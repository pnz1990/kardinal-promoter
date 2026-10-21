// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package agentrun implements the AgentRun reconciler (docs/design/
// 19-verdicts.md, section 8): it runs one attempt of an in-cluster verifier,
// a StepTemplate's agent image, for one Bundle, environment and check, and
// records the Job's result in AgentRun.status. The run's Pod writes a
// Verdict with its own credentials; the reconciler never reads it.
//
// Before the Job, the run must be bounded: a ready StepTemplate whose agent
// (an AgentIdentity) is ready, not suspended and in scope, and whose Secrets
// are labelled for the template. Kardinal does not borrow anyone's RBAC: each
// run gets a ServiceAccount of its own (kr-<run>), bound only to the chart's
// AgentRun roles (create Verdicts in the Pipeline's namespace; read the
// namespace under test), which is deleted, with its bindings, when the run
// ends. The chart's verdicts admission policy lets that ServiceAccount write
// only the run's own Verdict, from the run's Pod. The Pod is built here,
// never from user YAML: restricted Pod security, a short-lived projected
// token, the run's context as environment variables and a downward-API file,
// and a NetworkPolicy that lets it reach only DNS, the API server, the
// namespace under test and the template's egress.
//
// Why a CRD that owns its Job, not a Job node in the Graph: ledger G12. The
// Job and the NetworkPolicy have the AgentRun as controller owner, so
// garbage collection deletes them, and their Pods, with it.
package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/hookrun"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

const (
	// LabelAgentRun is set on the Job, its Pod and the NetworkPolicy: the
	// AgentRun's name.
	LabelAgentRun = "kardinal.io/agentrun"
	// AnnotationContext on the Pod holds the run's context as JSON; the Pod
	// reads it at /kardinal/context.json (a downward-API file).
	AnnotationContext = "kardinal.io/context"
	// ContextPath is where the Pod finds its context.
	ContextPath = "/kardinal/context.json"
	// LabelStepTemplateSecret on a Secret names the StepTemplate whose Pods
	// may read it (secretEnv).
	LabelStepTemplateSecret = "kardinal.io/step-template"
	// LabelAgentRunUID is set on the RoleBinding a run gets in the namespace
	// under test, which the AgentRun cannot own (another namespace): the
	// run's UID, to find and delete it.
	LabelAgentRunUID = "kardinal.io/agentrun-uid"
	// auditActionVerdictRecorded is the AuditEvent action the policy gate
	// writes when a Verdict enters a gate's verdicts.
	auditActionVerdictRecorded = "VerdictRecorded"

	// RunServiceAccountPrefix starts the name of a run's own ServiceAccount:
	// kr-<AgentRun name>. The verdicts admission policy keys on it.
	RunServiceAccountPrefix = "kr-"
	// FinalizerAccess holds an AgentRun until its access (ServiceAccount and
	// RoleBindings) is deleted.
	FinalizerAccess = "kardinal.io/agentrun-access"

	// DefaultTimeout bounds an attempt whose template sets none.
	DefaultTimeout = 30 * time.Minute
	// MaxTimeout is the longest timeout a template may set.
	MaxTimeout = 24 * time.Hour

	requeueRunning = 30 * time.Second
)

// Reconciler runs AgentRuns.
type Reconciler struct {
	client.Client

	// APIReader reads from the API server, uncached: Jobs the cache does not
	// have yet, the kubernetes EndpointSlice and labelled Secrets. Nil
	// means Client.
	APIReader client.Reader

	// WriterClusterRole is the chart's ClusterRole a run's ServiceAccount
	// is bound to in the Pipeline's namespace (create Verdicts), and
	// ReaderClusterRole the one in the namespace under test (read it). The
	// controller may bind only these two.
	WriterClusterRole, ReaderClusterRole string

	// ControllerNamespace is the controller's own namespace: no AgentRun
	// runs there.
	ControllerNamespace string

	// RequireDigest refuses StepTemplate images without a digest
	// (--step-template-require-digest).
	RequireDigest bool

	// NowFn returns the current time; nil means time.Now.
	NowFn func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

func (r *Reconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

var agentRunsResource = v1alpha1.GroupVersion.WithResource("agentruns").GroupResource()

// Reconcile runs one AgentRun forward. It is idempotent: each step finds
// what an earlier, possibly interrupted, reconcile did (the Job by its
// deterministic name and owner) before it acts, and every status write is
// optimistic-locked.
//
//	not applied by kro         → ignored
//	terminal                   → nothing
//	not started                → Succeeded when spec.recorded (a Verdict exists);
//	                             Skipped when the Bundle is Superseded or Rejected;
//	                             Failed when not bounded; else record start and deadline
//	no Job, no status.jobUID   → NetworkPolicy, then the Job (or adopt this run's Job)
//	Job gone or replaced       → Failed (never re-run)
//	Job Complete / Failed      → Succeeded / Failed
//	deadline passed            → Failed, Job deleted with its Pods
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, agentRunsResource, r.reconcile)
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("agentrun", req.Name).Str("namespace", req.Namespace).Logger()
	var run v1alpha1.AgentRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get agentrun %s: %w", req.Name, err)
	}
	if !run.DeletionTimestamp.IsZero() || terminal(run.Status.Phase) {
		// A finished or deleted run keeps no access: its ServiceAccount
		// and RoleBindings go.
		return ctrl.Result{}, r.revoke(ctx, &run)
	}
	bundle, why, err := r.genuine(ctx, &run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if why != "" {
		log.Warn().Str("reason", why).Msg("ignoring AgentRun not created by its Bundle's Graph")
		return ctrl.Result{}, nil
	}
	base := run.DeepCopy()
	if run.Status.StartedAt == nil {
		return r.start(ctx, log, base, &run, bundle)
	}
	job, found, err := r.job(ctx, &run)
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case !found && run.Status.JobUID == "":
		return r.createJob(ctx, log, base, &run, bundle)
	case !found:
		r.finish(&run, v1alpha1.AgentRunFailed, fmt.Sprintf("Job %s was deleted before it finished; the run is not run again", run.Status.JobName))
		return ctrl.Result{}, r.patch(ctx, base, &run)
	}
	if run.Status.JobUID == "" {
		return r.adopt(ctx, base, &run, job)
	}
	if string(job.UID) != run.Status.JobUID {
		r.finish(&run, v1alpha1.AgentRunFailed, fmt.Sprintf("Job %s was replaced by another Job of that name; the run is not run again", job.Name))
		return ctrl.Result{}, r.patch(ctx, base, &run)
	}
	return r.observe(ctx, log, base, &run, job)
}

// genuine returns run's Bundle when kro applied run for it (the kro node-id
// label and the Bundle's UID), else why not.
func (r *Reconciler) genuine(ctx context.Context, run *v1alpha1.AgentRun) (*v1alpha1.Bundle, string, error) {
	if _, ok := run.Labels[graph.LabelKRONodeID]; !ok {
		return nil, "no " + graph.LabelKRONodeID + " label: not applied by kro", nil
	}
	var b v1alpha1.Bundle
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.BundleName}, &b)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Sprintf("Bundle %s does not exist", run.Spec.BundleName), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("get bundle %s: %w", run.Spec.BundleName, err)
	}
	if got := run.Labels[graph.LabelBundleUID]; got == "" || got != string(b.UID) || run.Spec.BundleUID != string(b.UID) {
		return nil, fmt.Sprintf("%s %q is not the UID of Bundle %s", graph.LabelBundleUID, got, b.Name), nil
	}
	return &b, "", nil
}

// start decides whether the run runs at all, and records its start.
func (r *Reconciler) start(ctx context.Context, log zerolog.Logger, base, run *v1alpha1.AgentRun, bundle *v1alpha1.Bundle) (ctrl.Result, error) {
	if run.Spec.Recorded != "" {
		return r.recorded(ctx, log, base, run, bundle)
	}
	if halted(bundle) {
		r.finish(run, v1alpha1.AgentRunSkipped, "not run: Bundle "+bundle.Name+" is "+haltReason(bundle))
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	tmpl, agent, timeout, refused, err := r.bounds(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refused != "" {
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{Type: v1alpha1.AgentRunConditionBounded,
			Status: metav1.ConditionFalse, Reason: "Refused", Message: refused})
		r.finish(run, v1alpha1.AgentRunFailed, "refused: "+refused)
		log.Warn().Str("reason", refused).Msg("AgentRun refused")
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	// The finalizer first: the run's access is created only once the
	// AgentRun is held until it is deleted.
	if !controllerutil.ContainsFinalizer(run, FinalizerAccess) {
		patch := client.MergeFrom(run.DeepCopy())
		controllerutil.AddFinalizer(run, FinalizerAccess)
		if err := r.Patch(ctx, run, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer to agentrun %s: %w", run.Name, err)
		}
		return ctrl.Result{Requeue: true}, nil
	}
	now := metav1.NewTime(r.now())
	deadline := metav1.NewTime(now.Add(timeout))
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{Type: v1alpha1.AgentRunConditionBounded,
		Status: metav1.ConditionTrue, Reason: "Bounded",
		Message: fmt.Sprintf("agent %s, template %s: its own ServiceAccount, NetworkPolicy, %s timeout", agent.Name, tmpl.Name, timeout)})
	run.Status.Agent = agent.Name
	run.Status.Sponsor = agent.Spec.Sponsor.User
	run.Status.ServiceAccount = RunServiceAccountPrefix + run.Name
	run.Status.Phase = v1alpha1.AgentRunPending
	run.Status.JobName = run.Name
	run.Status.StartedAt = &now
	run.Status.Deadline = &deadline
	run.Status.Message = "starting"
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// templateTimeout is tmpl's timeout: DefaultTimeout when unset, at most
// MaxTimeout.
func templateTimeout(tmpl *v1alpha1.StepTemplate) (time.Duration, error) {
	if tmpl.Spec.Timeout == "" || tmpl.Spec.Timeout == "0" {
		return DefaultTimeout, nil
	}
	d, err := time.ParseDuration(tmpl.Spec.Timeout)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("StepTemplate %s: timeout %q is not a duration", tmpl.Name, tmpl.Spec.Timeout)
	}
	if d > MaxTimeout {
		return 0, fmt.Errorf("StepTemplate %s: timeout %s is longer than %s", tmpl.Name, d, MaxTimeout)
	}
	return d, nil
}

// bounds checks the run may start (docs/design/19-verdicts.md, section 8.4):
// its template, timeout and agent, or why it is refused.
func (r *Reconciler) bounds(ctx context.Context, run *v1alpha1.AgentRun) (*v1alpha1.StepTemplate, *v1alpha1.AgentIdentity,
	time.Duration, string, error) {
	if r.ControllerNamespace != "" && (run.Namespace == r.ControllerNamespace || run.Spec.TargetNamespace == r.ControllerNamespace) {
		return nil, nil, 0, "AgentRuns may not run in, or test, the controller's namespace " + r.ControllerNamespace, nil
	}
	tmpl, refused, err := r.template(ctx, run)
	if tmpl == nil {
		return nil, nil, 0, refused, err
	}
	if why := TemplateProblem(tmpl, r.RequireDigest); why != "" {
		return nil, nil, 0, "StepTemplate " + tmpl.Name + ": " + why, nil
	}
	timeout, err := templateTimeout(tmpl)
	if err != nil {
		return nil, nil, 0, err.Error(), nil
	}
	agent, refused, err := r.agentOf(ctx, tmpl, run)
	if agent == nil {
		return nil, nil, 0, refused, err
	}
	for _, s := range tmpl.Spec.SecretEnv {
		var sec corev1.Secret
		err := r.reader().Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: s.Secret}, &sec)
		if apierrors.IsNotFound(err) {
			return nil, nil, 0, fmt.Sprintf("Secret %s (secretEnv %s) does not exist", s.Secret, s.Name), nil
		}
		if err != nil {
			return nil, nil, 0, "", fmt.Errorf("get secret %s: %w", s.Secret, err)
		}
		if sec.Labels[LabelStepTemplateSecret] != tmpl.Name {
			return nil, nil, 0, fmt.Sprintf("Secret %s is not labelled %s=%s: a template reads only the Secrets labelled for it",
				s.Secret, LabelStepTemplateSecret, tmpl.Name), nil
		}
	}
	return tmpl, agent, timeout, "", nil
}

// template is run's StepTemplate, or why there is none.
func (r *Reconciler) template(ctx context.Context, run *v1alpha1.AgentRun) (*v1alpha1.StepTemplate, string, error) {
	var tmpl v1alpha1.StepTemplate
	if err := r.Get(ctx, types.NamespacedName{Name: run.Spec.Template}, &tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "StepTemplate " + run.Spec.Template + " does not exist", nil
		}
		return nil, "", fmt.Errorf("get steptemplate %s: %w", run.Spec.Template, err)
	}
	return &tmpl, "", nil
}

// agentOf is the AgentIdentity tmpl names, when it may verify run, or why
// not.
func (r *Reconciler) agentOf(ctx context.Context, tmpl *v1alpha1.StepTemplate, run *v1alpha1.AgentRun) (*v1alpha1.AgentIdentity, string, error) {
	var agent v1alpha1.AgentIdentity
	if err := r.Get(ctx, types.NamespacedName{Name: tmpl.Spec.Agent}, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "agent " + tmpl.Spec.Agent + " (StepTemplate " + tmpl.Name + ") is not registered (no AgentIdentity)", nil
		}
		return nil, "", fmt.Errorf("get agentidentity %s: %w", tmpl.Spec.Agent, err)
	}
	if why := agentProblem(&agent, run); why != "" {
		return nil, "agent " + agent.Name + " " + why, nil
	}
	return &agent, "", nil
}

// recorded finishes a run whose Verdict exists (spec.recorded: kro created
// it again after it was deleted) without running it (#2345). It names the
// agent and sponsor its StepTemplate names, as a run that starts does, and
// its ServiceAccount, only when that agent may still verify the run and
// every record kardinal wrote of the run's Verdict (recordedActors) names
// that same agent and sponsor, and there is one: the Graph counts a
// run-bound Verdict only when its run names an allowed agent, and the
// evidence pack traces the Verdict through it. Otherwise (the template's
// agent or the sponsor changed since the run, or no record is left) it
// names none and says why, and the Verdict does not count: a Verdict one
// agent's run wrote is never counted as another's.
func (r *Reconciler) recorded(ctx context.Context, log zerolog.Logger, base, run *v1alpha1.AgentRun, bundle *v1alpha1.Bundle) (ctrl.Result, error) {
	msg := "verdict already recorded (" + run.Spec.Recorded + "); not run again"
	tmpl, refused, err := r.template(ctx, run)
	var agent *v1alpha1.AgentIdentity
	if tmpl != nil {
		agent, refused, err = r.agentOf(ctx, tmpl, run)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if agent != nil {
		want := v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: graph.RunServiceAccountUser(run.Namespace, run.Name),
			Agent: agent.Name, Sponsor: agent.Spec.Sponsor.User}
		actors, verifiers, err := r.recordedActors(ctx, run, bundle)
		if err != nil {
			return ctrl.Result{}, err
		}
		agrees := len(actors)+len(verifiers) > 0
		for _, a := range actors {
			agrees = agrees && a.Kind == want.Kind && a.Agent == want.Agent && a.Sponsor == want.Sponsor
		}
		for _, v := range verifiers {
			agrees = agrees && v == actor.Describe(want)
		}
		if !agrees {
			agent, refused = nil, fmt.Sprintf("agent changed since the run, or unknown: no record of its Verdict names %s "+
				"(%d VerdictRecorded AuditEvents, %d receipts name the run)", actor.Describe(want), len(actors), len(verifiers))
		}
	}
	if agent == nil {
		msg += "; it names no agent: " + refused
		log.Warn().Str("reason", refused).Msg("re-created AgentRun names no agent; its Verdict does not count")
	} else {
		run.Status.Agent = agent.Name
		run.Status.Sponsor = agent.Spec.Sponsor.User
		run.Status.ServiceAccount = RunServiceAccountPrefix + run.Name
	}
	r.finish(run, v1alpha1.AgentRunSucceeded, msg)
	return ctrl.Result{}, r.patch(ctx, base, run)
}

// recordedActors are what the records kardinal wrote of run's Verdict say
// its writer was, read from the API server: the actor of each
// VerdictRecorded AuditEvent of the run's Bundle whose principal is the
// run's ServiceAccount (the policy gate writes one when the Verdict enters
// a gate's verdicts, with the actor the first run named), and the verifier
// of the verdict named after the run in each receipt of the Bundle (signed,
// written when an environment was promoted). Only the controller writes
// either, and neither changes after it is written.
//
// Names repeat: a Bundle deleted and created again under its name has runs,
// Verdicts and gates of the same names, and the records of the earlier one
// stay. So a receipt counts only when it names this Bundle's UID, and an
// AuditEvent (which names no UID) only when it is dated after the second
// this Bundle was created in: both times are whole seconds, the record's
// from the controller's clock and the creation's from the API server's, and
// this Bundle's own record comes only after its run has run. An earlier
// Bundle's record dated within the controller clock's lead over the API
// server's can still pass, until gate AuditEvents name the Bundle's UID
// (#2380).
func (r *Reconciler) recordedActors(ctx context.Context, run *v1alpha1.AgentRun, bundle *v1alpha1.Bundle) ([]v1alpha1.Actor, []string, error) {
	principal := graph.RunServiceAccountUser(run.Namespace, run.Name)
	var events v1alpha1.AuditEventList
	if err := r.reader().List(ctx, &events, client.InNamespace(run.Namespace), client.MatchingLabels{
		"kardinal.io/bundle": run.Spec.BundleName, "kardinal.io/action": auditActionVerdictRecorded}); err != nil {
		return nil, nil, fmt.Errorf("list VerdictRecorded AuditEvents of bundle %s: %w", run.Spec.BundleName, err)
	}
	var actors []v1alpha1.Actor
	for _, ev := range events.Items {
		if a := ev.Spec.Actor; a != nil && a.Principal == principal && ev.Spec.BundleName == run.Spec.BundleName &&
			ev.Spec.Action == auditActionVerdictRecorded && ev.Spec.Timestamp.After(bundle.CreationTimestamp.Time) {
			actors = append(actors, *a)
		}
	}
	// Receipts are optional (receipts.enabled): without them the controller
	// may not read any, and there is none to read.
	var receipts v1alpha1.PromotionReceiptList
	err := r.reader().List(ctx, &receipts, client.InNamespace(run.Namespace),
		client.MatchingLabels{"kardinal.io/bundle": run.Spec.BundleName})
	if err != nil && !apierrors.IsForbidden(err) && !meta.IsNoMatchError(err) {
		return nil, nil, fmt.Errorf("list receipts of bundle %s: %w", run.Spec.BundleName, err)
	}
	var verifiers []string
	for _, rc := range receipts.Items {
		st, err := receipt.Parse([]byte(rc.Spec.Statement))
		if err != nil || st.Predicate.Kardinal.Version.BundleUID != run.Spec.BundleUID {
			continue // a statement it cannot read, or an earlier Bundle's, names no one
		}
		for _, g := range st.Predicate.Kardinal.Gates {
			for _, v := range g.Verdicts {
				if v.Name == run.Name {
					verifiers = append(verifiers, v.Verifier)
				}
			}
		}
	}
	return actors, verifiers, nil
}

// agentProblem is why agent may not verify run, or "": it must be ready,
// not suspended, and its scope must cover the run's namespace, Pipeline and
// environment (docs/design/17-actors.md, section 3).
func agentProblem(agent *v1alpha1.AgentIdentity, run *v1alpha1.AgentRun) string {
	orAll := func(l []string) []string {
		if len(l) == 0 {
			return []string{"*"}
		}
		return l
	}
	switch {
	case agent.Spec.Suspended:
		return "is suspended"
	case !agent.Status.Ready:
		return "is not ready: " + agent.Status.Reason
	case !actor.Matches(agent.Spec.Scope.Namespaces, run.Namespace):
		return "may not act in namespace " + run.Namespace + " (scope.namespaces)"
	case !actor.Matches(orAll(agent.Spec.Scope.Pipelines), run.Spec.PipelineName):
		return "may not act on pipeline " + run.Spec.PipelineName + " (scope.pipelines)"
	case !actor.Matches(orAll(agent.Spec.Scope.Environments), run.Spec.Environment):
		return "may not act in environment " + run.Spec.Environment + " (scope.environments)"
	}
	return ""
}

// TemplateProblem is why tmpl cannot be run, or "". The StepTemplate
// reconciler writes it to status.reason; the AgentRun reconciler checks it
// again before each run.
func TemplateProblem(tmpl *v1alpha1.StepTemplate, requireDigest bool) string {
	if tmpl.Spec.Output != "" && tmpl.Spec.Output != v1alpha1.StepTemplateOutputVerdict {
		return "output " + tmpl.Spec.Output + " is not supported; use Verdict"
	}
	if requireDigest && !strings.Contains(tmpl.Spec.Image, "@sha256:") {
		return "image " + tmpl.Spec.Image + " has no digest (--step-template-require-digest)"
	}
	if _, err := templateTimeout(tmpl); err != nil {
		return err.Error()
	}
	for _, e := range tmpl.Spec.Env {
		if strings.HasPrefix(e.Name, "KARDINAL_") {
			return "env " + e.Name + ": names starting with KARDINAL_ are the run's context"
		}
	}
	for _, e := range tmpl.Spec.SecretEnv {
		if strings.HasPrefix(e.Name, "KARDINAL_") {
			return "secretEnv " + e.Name + ": names starting with KARDINAL_ are the run's context"
		}
	}
	for _, e := range tmpl.Spec.Egress {
		if _, err := parseCIDR(e.CIDR); err != nil {
			return "egress " + e.CIDR + ": " + err.Error()
		}
	}
	return ""
}

// job returns the run's Job, from the cache and, when the cache does not
// have it, from the API server.
func (r *Reconciler) job(ctx context.Context, run *v1alpha1.AgentRun) (*batchv1.Job, bool, error) {
	key := types.NamespacedName{Name: run.Status.JobName, Namespace: run.Namespace}
	var job batchv1.Job
	err := r.Get(ctx, key, &job)
	if apierrors.IsNotFound(err) {
		err = r.reader().Get(ctx, key, &job)
	}
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get job %s: %w", key.Name, err)
	}
	return &job, true, nil
}

// createJob creates the run's NetworkPolicy and then its Job.
func (r *Reconciler) createJob(ctx context.Context, log zerolog.Logger, base, run *v1alpha1.AgentRun, bundle *v1alpha1.Bundle) (ctrl.Result, error) {
	if r.expired(run) {
		r.finish(run, v1alpha1.AgentRunFailed, "timed out before its Job was created")
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if halted(bundle) {
		r.finish(run, v1alpha1.AgentRunSkipped, "not run: Bundle "+bundle.Name+" is "+haltReason(bundle))
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	var tmpl v1alpha1.StepTemplate
	if err := r.Get(ctx, types.NamespacedName{Name: run.Spec.Template}, &tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			r.finish(run, v1alpha1.AgentRunFailed, "StepTemplate "+run.Spec.Template+" was deleted before the Job was created")
			return ctrl.Result{}, r.patch(ctx, base, run)
		}
		return ctrl.Result{}, fmt.Errorf("get steptemplate %s: %w", run.Spec.Template, err)
	}
	if err := r.ensureAccess(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	policy, err := r.networkPolicy(ctx, run, &tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensure(ctx, run, policy); err != nil {
		return ctrl.Result{}, err
	}
	job, err := r.jobFor(ctx, run, &tmpl, bundle)
	if err != nil {
		r.finish(run, v1alpha1.AgentRunFailed, err.Error())
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if err := controllerutil.SetControllerReference(run, job, r.Scheme()); err != nil {
		return ctrl.Result{}, fmt.Errorf("set owner of job %s: %w", job.Name, err)
	}
	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing batchv1.Job
			getErr := r.reader().Get(ctx, client.ObjectKeyFromObject(job), &existing)
			if apierrors.IsNotFound(getErr) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			if getErr != nil {
				return ctrl.Result{}, fmt.Errorf("get existing job %s: %w", job.Name, getErr)
			}
			return r.adopt(ctx, base, run, &existing)
		}
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			r.finish(run, v1alpha1.AgentRunFailed, fmt.Sprintf("create Job %s: %v", job.Name, err))
			return ctrl.Result{}, r.patch(ctx, base, run)
		}
		return ctrl.Result{}, fmt.Errorf("create job %s: %w", job.Name, err)
	}
	log.Info().Str("job", job.Name).Str("template", tmpl.Name).Msg("agent Job created")
	run.Status.JobUID = string(job.UID)
	run.Status.Phase = v1alpha1.AgentRunRunning
	run.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.untilDeadline(run)}, nil
}

// ensure creates obj, owned by run when it is in run's namespace, unless an
// object of its name that run owns (or, in another namespace, that carries
// run's UID label) exists. One that is not run's is an error.
func (r *Reconciler) ensure(ctx context.Context, run *v1alpha1.AgentRun, obj client.Object) error {
	kind := fmt.Sprintf("%T", obj)
	if obj.GetNamespace() == run.Namespace {
		if err := controllerutil.SetControllerReference(run, obj, r.Scheme()); err != nil {
			return fmt.Errorf("set owner of %s %s: %w", kind, obj.GetName(), err)
		}
	}
	err := r.Create(ctx, obj)
	if err == nil || !apierrors.IsAlreadyExists(err) {
		if err != nil {
			return fmt.Errorf("create %s %s/%s: %w", kind, obj.GetNamespace(), obj.GetName(), err)
		}
		return nil
	}
	existing := obj.DeepCopyObject().(client.Object)
	if err := r.reader().Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		return fmt.Errorf("get %s %s/%s: %w", kind, obj.GetNamespace(), obj.GetName(), err)
	}
	owner := metav1.GetControllerOf(existing)
	mine := owner != nil && owner.UID == run.UID
	if obj.GetNamespace() != run.Namespace {
		mine = existing.GetLabels()[LabelAgentRunUID] == string(run.UID)
	}
	if !mine {
		return fmt.Errorf("a %s named %s/%s exists and is not AgentRun %s's", kind, obj.GetNamespace(), obj.GetName(), run.Name)
	}
	return nil
}

// ensureAccess creates the run's own ServiceAccount and binds it to the
// chart's two AgentRun ClusterRoles: the writer in the Pipeline's namespace
// (create Verdicts) and the reader in the namespace under test. Nothing else
// is granted, and nothing is borrowed from the agent's own identities.
func (r *Reconciler) ensureAccess(ctx context.Context, run *v1alpha1.AgentRun) error {
	if r.WriterClusterRole == "" || r.ReaderClusterRole == "" {
		return fmt.Errorf("agentrun %s: the AgentRun ClusterRoles are not configured (--agentrun-writer-clusterrole, --agentrun-reader-clusterrole)", run.Name)
	}
	sa := run.Status.ServiceAccount
	labels := map[string]string{LabelAgentRun: run.Name, LabelAgentRunUID: string(run.UID)}
	no := false
	if err := r.ensure(ctx, run, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: run.Namespace, Labels: labels},
		AutomountServiceAccountToken: &no}); err != nil {
		return err
	}
	subject := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: run.Namespace}}
	for _, b := range []struct{ ns, role string }{{run.Namespace, r.WriterClusterRole}, {run.Spec.TargetNamespace, r.ReaderClusterRole}} {
		name := sa
		if b.ns == run.Namespace && b.role == r.ReaderClusterRole {
			name = sa + "-read" // the namespace under test is the Pipeline's
		}
		rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.ns, Labels: labels},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: b.role},
			Subjects: subject}
		if err := r.ensure(ctx, run, rb); err != nil {
			return err
		}
	}
	return nil
}

// revoke deletes the run's ServiceAccount (its tokens stop working) and its
// RoleBindings, then lets the AgentRun go. Idempotent: what is gone is
// skipped.
func (r *Reconciler) revoke(ctx context.Context, run *v1alpha1.AgentRun) error {
	if !controllerutil.ContainsFinalizer(run, FinalizerAccess) {
		return nil
	}
	sel := client.MatchingLabels{LabelAgentRunUID: string(run.UID)}
	for _, ns := range []string{run.Spec.TargetNamespace, run.Namespace} {
		var rbs rbacv1.RoleBindingList
		if err := r.reader().List(ctx, &rbs, client.InNamespace(ns), sel); err != nil {
			return fmt.Errorf("list rolebindings of agentrun %s: %w", run.Name, err)
		}
		for i := range rbs.Items {
			if err := r.Delete(ctx, &rbs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete rolebinding %s/%s: %w", ns, rbs.Items[i].Name, err)
			}
		}
	}
	if sa := run.Status.ServiceAccount; sa != "" {
		obj := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: run.Namespace}}
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete serviceaccount %s: %w", sa, err)
		}
	}
	patch := client.MergeFrom(run.DeepCopy())
	controllerutil.RemoveFinalizer(run, FinalizerAccess)
	if err := r.Patch(ctx, run, patch); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove finalizer of agentrun %s: %w", run.Name, err)
	}
	return nil
}

// networkPolicy is the run's NetworkPolicy: its Pod gets no ingress, and
// egress only to DNS, the API server, every Pod of the target namespace and
// the template's egress.
func (r *Reconciler) networkPolicy(ctx context.Context, run *v1alpha1.AgentRun, tmpl *v1alpha1.StepTemplate) (*networkingv1.NetworkPolicy, error) {
	udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
	dns := intstr.FromInt32(53)
	egress := []networkingv1.NetworkPolicyEgressRule{
		{
			To: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}},
		},
		{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": run.Spec.TargetNamespace}}}}},
	}
	api, err := r.apiServerEgress(ctx)
	if err != nil {
		return nil, err
	}
	egress = append(egress, api...)
	for _, e := range tmpl.Spec.Egress {
		cidr, err := parseCIDR(e.CIDR)
		if err != nil {
			return nil, fmt.Errorf("steptemplate %s egress %s: %w", tmpl.Name, e.CIDR, err)
		}
		rule := networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}}}
		for _, p := range e.Ports {
			port := intstr.FromInt32(p)
			rule.Ports = append(rule.Ports, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &port})
		}
		egress = append(egress, rule)
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: run.Namespace, Labels: map[string]string{LabelAgentRun: run.Name}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelAgentRun: run.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{},
			Egress:      egress,
		},
	}, nil
}

// apiServerEgress are the egress rules to the Kubernetes API server: the
// addresses and ports of the default/kubernetes EndpointSlice (what a Pod's
// connection to the kubernetes Service is translated to).
func (r *Reconciler) apiServerEgress(ctx context.Context) ([]networkingv1.NetworkPolicyEgressRule, error) {
	var slice discoveryv1.EndpointSlice
	if err := r.reader().Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "kubernetes"}, &slice); err != nil {
		return nil, fmt.Errorf("read the API server's addresses (EndpointSlice default/kubernetes): %w", err)
	}
	var rules []networkingv1.NetworkPolicyEgressRule
	var ports []networkingv1.NetworkPolicyPort
	tcp := corev1.ProtocolTCP
	for _, p := range slice.Ports {
		if p.Port != nil {
			port := intstr.FromInt32(*p.Port)
			ports = append(ports, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &port})
		}
	}
	for _, ep := range slice.Endpoints {
		for _, addr := range ep.Addresses {
			cidr, err := parseCIDR(addr)
			if err != nil {
				continue
			}
			rules = append(rules, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}}, Ports: ports})
		}
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("the EndpointSlice default/kubernetes lists no API server address")
	}
	return rules, nil
}

// runContext is what a run's Pod is told (docs/design/19-verdicts.md,
// section 8.5).
type runContext struct {
	Pipeline        string                     `json:"pipeline"`
	Bundle          string                     `json:"bundle"`
	BundleUID       string                     `json:"bundleUID"`
	Version         string                     `json:"version"`
	Environment     string                     `json:"environment"`
	TargetNamespace string                     `json:"targetNamespace"`
	Check           string                     `json:"check"`
	Run             int                        `json:"run"`
	Attempt         int                        `json:"attempt"`
	Deadline        string                     `json:"deadline"`
	Images          []v1alpha1.ImageRef        `json:"images"`
	Provenance      *v1alpha1.BundleProvenance `json:"provenance,omitempty"`
	// Verdict is the Verdict to write, complete but for result, summary,
	// evidenceURL, findings, startedAt and durationSeconds.
	Verdict map[string]interface{} `json:"verdict"`
}

// verdictTemplate is the Verdict a run's Pod writes, without its result. It
// has no actor stamp: the run's ServiceAccount is not an agent's subject,
// and the Graph names the run's agent as the Verdict's actor (from the
// AgentRun's status).
func verdictTemplate(run *v1alpha1.AgentRun, bundle *v1alpha1.Bundle, by string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": v1alpha1.GroupVersion.String(),
		"kind":       "Verdict",
		"metadata": map[string]interface{}{
			"name":      run.Name,
			"namespace": run.Namespace,
			"labels": map[string]string{"kardinal.io/bundle": bundle.Name, "kardinal.io/environment": run.Spec.Environment,
				graph.LabelCheck: run.Spec.Check},
			"ownerReferences": []map[string]interface{}{{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Bundle",
				"name": bundle.Name, "uid": string(bundle.UID)}},
		},
		"spec": map[string]interface{}{
			"pipeline": run.Spec.PipelineName, "bundle": bundle.Name, "bundleUID": string(bundle.UID),
			"environment": run.Spec.Environment, "check": run.Spec.Check, "by": by,
			"run": map[string]interface{}{"name": run.Name, "index": run.Spec.Run, "attempt": run.Spec.Attempt},
		},
	}
}

// jobFor builds the run's Job: one restricted Pod running the template's
// image as its ServiceAccount, with the run's context.
func (r *Reconciler) jobFor(ctx context.Context, run *v1alpha1.AgentRun, tmpl *v1alpha1.StepTemplate,
	bundle *v1alpha1.Bundle) (*batchv1.Job, error) {
	timeout := run.Status.Deadline.Sub(run.Status.StartedAt.Time)
	by := actor.ServiceAccountPrefix + run.Namespace + ":" + run.Status.ServiceAccount
	rc := runContext{Pipeline: run.Spec.PipelineName, Bundle: bundle.Name, BundleUID: string(bundle.UID),
		Version: run.Spec.Version, Environment: run.Spec.Environment, TargetNamespace: run.Spec.TargetNamespace,
		Check: run.Spec.Check, Run: run.Spec.Run, Attempt: run.Spec.Attempt,
		Deadline: run.Status.Deadline.UTC().Format(time.RFC3339), Images: bundle.Spec.Images,
		Provenance: bundle.Spec.Provenance, Verdict: verdictTemplate(run, bundle, by)}
	raw, err := json.Marshal(rc)
	if err != nil {
		return nil, fmt.Errorf("agentrun %s: context: %w", run.Name, err)
	}
	images, err := json.Marshal(bundle.Spec.Images)
	if err != nil {
		return nil, fmt.Errorf("agentrun %s: images: %w", run.Name, err)
	}
	env := []corev1.EnvVar{
		{Name: "KARDINAL_PIPELINE", Value: rc.Pipeline}, {Name: "KARDINAL_BUNDLE", Value: rc.Bundle},
		{Name: "KARDINAL_BUNDLE_UID", Value: rc.BundleUID}, {Name: "KARDINAL_VERSION", Value: rc.Version},
		{Name: "KARDINAL_ENVIRONMENT", Value: rc.Environment}, {Name: "KARDINAL_TARGET_NAMESPACE", Value: rc.TargetNamespace},
		{Name: "KARDINAL_CHECK", Value: rc.Check}, {Name: "KARDINAL_RUN", Value: strconv.Itoa(rc.Run)},
		{Name: "KARDINAL_ATTEMPT", Value: strconv.Itoa(rc.Attempt)}, {Name: "KARDINAL_IMAGES", Value: string(images)},
		{Name: "KARDINAL_DEADLINE", Value: rc.Deadline}, {Name: "KARDINAL_CONTEXT", Value: ContextPath},
	}
	for _, e := range tmpl.Spec.Env {
		env = append(env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	for _, e := range tmpl.Spec.SecretEnv {
		env = append(env, corev1.EnvVar{Name: e.Name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: e.Secret}, Key: e.Key}}})
	}
	no, yes := false, true
	uid := int64(65532)
	deadline := int64(timeout.Seconds())
	backoff := int32(0)
	tokenSeconds := max(int64(600), deadline+300)
	labels := map[string]string{LabelAgentRun: run.Name, hookrun.LabelRunJob: "agentrun",
		"kardinal.io/pipeline": run.Spec.PipelineName, "kardinal.io/bundle": run.Spec.BundleName,
		"kardinal.io/environment": run.Spec.Environment, graph.LabelCheck: run.Spec.Check}
	pod := corev1.PodSpec{
		ServiceAccountName:           run.Status.ServiceAccount,
		AutomountServiceAccountToken: &no,
		RestartPolicy:                corev1.RestartPolicyNever,
		EnableServiceLinks:           &no,
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{
			Name: "agent", Image: tmpl.Spec.Image, Command: tmpl.Spec.Command, Args: tmpl.Spec.Args, Env: env,
			Resources: tmpl.Spec.Resources,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes,
				RunAsNonRoot: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "token", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true},
				{Name: "context", MountPath: "/kardinal", ReadOnly: true},
				{Name: "tmp", MountPath: "/tmp"},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: "token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
				{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: &tokenSeconds}},
				{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
					Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
				{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{Path: "namespace",
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}}}},
			}}}},
			{Name: "context", VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{{Path: "context.json",
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['" + AnnotationContext + "']"}}}}}},
			{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	if tmpl.Spec.RuntimeClassName != "" {
		rc := tmpl.Spec.RuntimeClassName
		pod.RuntimeClassName = &rc
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: run.Status.JobName, Namespace: run.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{AnnotationContext: string(raw)}},
				Spec:       pod,
			},
		},
	}, nil
}

// adopt records an existing Job named after the run as its own when the run
// is its controller owner, else fails the run.
func (r *Reconciler) adopt(ctx context.Context, base, run *v1alpha1.AgentRun, job *batchv1.Job) (ctrl.Result, error) {
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.UID != run.UID {
		r.finish(run, v1alpha1.AgentRunFailed, fmt.Sprintf("a Job named %s exists and is not owned by this AgentRun", job.Name))
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	run.Status.JobUID = string(job.UID)
	run.Status.Phase = v1alpha1.AgentRunRunning
	run.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// observe copies the Job's outcome into the run and enforces the deadline.
func (r *Reconciler) observe(ctx context.Context, log zerolog.Logger, base, run *v1alpha1.AgentRun, job *batchv1.Job) (ctrl.Result, error) {
	if c := jobCondition(job, batchv1.JobComplete); c != nil {
		r.finish(run, v1alpha1.AgentRunSucceeded, "Job "+job.Name+" completed")
		log.Info().Str("job", job.Name).Msg("agent run finished")
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if c := jobCondition(job, batchv1.JobFailed); c != nil {
		msg := fmt.Sprintf("Job %s failed: %s", job.Name, c.Reason)
		if c.Message != "" {
			msg += ": " + c.Message
		}
		r.finish(run, v1alpha1.AgentRunFailed, msg)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if r.expired(run) {
		policy := metav1.DeletePropagationBackground
		if err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete timed-out job %s: %w", job.Name, err)
		}
		r.finish(run, v1alpha1.AgentRunFailed, fmt.Sprintf("Job %s did not finish before the %s timeout; it was deleted",
			job.Name, run.Status.Deadline.Sub(run.Status.StartedAt.Time)))
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	run.Status.Phase = v1alpha1.AgentRunRunning
	run.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.untilDeadline(run)}, nil
}

// finish moves run to a terminal phase once.
func (r *Reconciler) finish(run *v1alpha1.AgentRun, phase, msg string) {
	if terminal(run.Status.Phase) {
		return
	}
	now := metav1.NewTime(r.now())
	run.Status.Phase = phase
	run.Status.Message = msg
	run.Status.FinishedAt = &now
	observability.AgentRunsTotal.WithLabelValues(run.Spec.Template, phase).Inc()
}

func (r *Reconciler) expired(run *v1alpha1.AgentRun) bool {
	return run.Status.Deadline != nil && !r.now().Before(run.Status.Deadline.Time)
}

func (r *Reconciler) untilDeadline(run *v1alpha1.AgentRun) time.Duration {
	if run.Status.Deadline == nil {
		return requeueRunning
	}
	d := run.Status.Deadline.Sub(r.now())
	if d <= 0 {
		return time.Second
	}
	return min(d, requeueRunning)
}

// patch writes run's status when it differs from base, optimistic-locked.
func (r *Reconciler) patch(ctx context.Context, base, run *v1alpha1.AgentRun) error {
	if statusEqual(&base.Status, &run.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, run, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch agentrun %s status: %w", run.Name, err)
	}
	return nil
}

func statusEqual(a, b *v1alpha1.AgentRunStatus) bool {
	return a.Phase == b.Phase && a.Message == b.Message && a.Agent == b.Agent && a.Sponsor == b.Sponsor &&
		a.ServiceAccount == b.ServiceAccount && a.JobName == b.JobName &&
		a.JobUID == b.JobUID && a.StartedAt.Equal(b.StartedAt) && a.Deadline.Equal(b.Deadline) &&
		a.FinishedAt.Equal(b.FinishedAt) && slices.EqualFunc(a.Conditions, b.Conditions, func(x, y metav1.Condition) bool {
		return x.Type == y.Type && x.Status == y.Status && x.Reason == y.Reason && x.Message == y.Message
	})
}

func terminal(phase string) bool {
	return phase == v1alpha1.AgentRunSucceeded || phase == v1alpha1.AgentRunFailed || phase == v1alpha1.AgentRunSkipped
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == t && job.Status.Conditions[i].Status == corev1.ConditionTrue {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

func halted(b *v1alpha1.Bundle) bool {
	return b.Spec.Rejected != nil || b.Status.Phase == "Rejected" || b.Status.Phase == "Superseded"
}

func haltReason(b *v1alpha1.Bundle) string {
	if b.Status.Phase == "Superseded" {
		return "Superseded"
	}
	return "Rejected"
}

// SetupWithManager registers the reconciler; Job events wake their owner.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AgentRun{}).
		Owns(&batchv1.Job{})
	return shard.Active().Complete(b, tracing.WrapReconciler("agentrun", r), &v1alpha1.AgentRunList{})
}
