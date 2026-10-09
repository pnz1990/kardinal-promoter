// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package hookrun implements the HookRun reconciler: it runs a Pipeline
// hook's Job once for a Bundle and environment and writes the result to
// HookRun.status. The Bundle's Graph creates the HookRun and mirrors its
// status onto the PromotionStep (spec.live.hooks); the PromotionStep
// reconciler reads only that copy.
//
// Why a CRD and not a Job node in the Graph (ledger G12): kro deletes and
// prunes without a propagation policy, so a Job node's Pods outlive it; kro
// re-creates a deleted Job, which runs the hook again; and an edited Job
// template is an immutable-field error that stops the whole Graph. Here the
// Job has the HookRun as its controller owner (garbage collection deletes it
// and its Pods), a finished HookRun never creates a Job again (the CRD
// refuses to change a terminal phase), a spec edit after the start is
// reported, not applied, and a HookRun removed from the Graph while its Job
// runs is held by a finalizer until the Job ends or times out.
package hookrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/rs/zerolog"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

const (
	// ConditionSpecChangedAfterStart is True when spec.job or spec.timeout
	// changed after the Job was created. The change is not applied.
	ConditionSpecChangedAfterStart = "SpecChangedAfterStart"

	// LabelHookRun is set on the Job and its Pods: the HookRun name.
	LabelHookRun = "kardinal.io/hookrun"

	// Finalizer holds a HookRun that is deleted (its Bundle deleted, or the
	// hook renamed or removed from the Pipeline) while its Job runs, until the
	// Job ends or passes its deadline: a migration is never cut off halfway,
	// and the HookRun that replaces it waits for it (siblingRunning).
	Finalizer = "kardinal.io/hookrun-job"

	// DefaultServiceAccount is the only Pod ServiceAccount a hook may use
	// when the controller is given no allowlist.
	DefaultServiceAccount = "default"

	// requeueRunning is the fallback poll while the Job runs; the Job watch
	// normally wakes the HookRun first.
	requeueRunning = 30 * time.Second
	// requeueSibling is the poll while another run of the same hook slot
	// finishes.
	requeueSibling = 10 * time.Second
)

// Reconciler runs HookRun Jobs.
type Reconciler struct {
	client.Client

	// APIReader reads from the API server, uncached (mgr.GetAPIReader()). A
	// Job the cache does not have is looked up there before it is taken as
	// deleted: the cache can lag a Job just created, and it holds only Jobs
	// labelled kardinal.io/hookrun. Nil means Client.
	APIReader client.Reader

	// AllowedServiceAccounts are the Pod ServiceAccount names a hook's Job
	// may run as (--hook-service-accounts). Empty means only "default".
	AllowedServiceAccounts []string

	// GraphServiceAccount is the ServiceAccount kro impersonates for Graphs.
	// A hook may never run as it, whatever the allowlist says: it can write
	// every object a Graph renders.
	GraphServiceAccount string

	// ControllerNamespace is the controller's own namespace. A hook there is
	// refused: it would run next to the controller's credentials.
	ControllerNamespace string

	// PodSecurityLevel is the Pod Security Standard a hook Pod must meet
	// (--hook-pod-security-level): baseline (the default, ""), restricted,
	// or privileged (no Pod checks). Below privileged, nodeName is refused
	// as well.
	PodSecurityLevel string

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

var hookRunsResource = v1alpha1.GroupVersion.WithResource("hookruns").GroupResource()

// Reconcile runs one HookRun forward. It is idempotent: each step finds what
// an earlier, possibly interrupted, reconcile did (the Job by its
// deterministic name and owner) before it acts, and every status write is
// optimistic-locked, so a reconcile working from a stale copy fails and
// retries instead of overwriting a newer phase.
//
//	being deleted            → hold (finalizer) while the Job runs and the deadline is ahead
//	no status.specHash       → Skipped when spec.stepAdvanced; else validate,
//	                           add the finalizer, record specHash, startedAt, deadline
//	another run of the slot  → wait while a sibling's Job runs, or an older sibling is Pending
//	no Job, no status.jobUID → create the Job (or adopt the one this HookRun owns)
//	Job gone or replaced     → Failed (never re-run)
//	Job Complete / Failed    → Succeeded / Failed
//	deadline passed          → Failed, Job deleted with its Pods
//	terminal                 → nothing more (only SpecChangedAfterStart is kept current)
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, hookRunsResource, r.reconcile)
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("hookrun", req.Name).Str("namespace", req.Namespace).Logger()
	var hr v1alpha1.HookRun
	if err := r.Get(ctx, req.NamespacedName, &hr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get hookrun %s: %w", req.Name, err)
	}
	if !hr.DeletionTimestamp.IsZero() {
		return r.deleting(ctx, log, &hr)
	}
	if why, err := r.notGenuine(ctx, &hr); err != nil || why != "" {
		if err != nil {
			return ctrl.Result{}, err
		}
		// Not created by a Bundle's Graph: never run its Job. The mirror
		// ignores it too, so it is no result for any step.
		log.Warn().Str("reason", why).Msg("ignoring HookRun not created by its Bundle's Graph")
		return ctrl.Result{}, nil
	}
	base := hr.DeepCopy()
	hash := specHash(&hr.Spec)
	if hr.Status.SpecHash != "" {
		r.markSpecChange(&hr, hash)
	}

	if terminal(hr.Status.Phase) {
		return ctrl.Result{}, r.patch(ctx, base, &hr)
	}

	if hr.Status.SpecHash == "" {
		return r.start(ctx, log, base, &hr, hash)
	}

	job, found, err := r.job(ctx, &hr)
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case !found && hr.Status.JobUID == "":
		if wait, who, err := r.siblingRunning(ctx, &hr); err != nil || wait {
			if err != nil {
				return ctrl.Result{}, err
			}
			hr.Status.Message = fmt.Sprintf("waiting for HookRun %s (same environment and phase) to finish its Job", who)
			return ctrl.Result{RequeueAfter: requeueSibling}, r.patch(ctx, base, &hr)
		}
		return r.createJob(ctx, log, base, &hr)
	case !found:
		r.finish(&hr, v1alpha1.HookRunFailed, fmt.Sprintf(
			"Job %s was deleted before it finished; the hook is not run again", hr.Status.JobName))
		return ctrl.Result{}, r.patch(ctx, base, &hr)
	}
	if hr.Status.JobUID == "" {
		// A crash between the create and the status write: adopt the Job if
		// it is this HookRun's.
		return r.adopt(ctx, base, &hr, job)
	}
	if string(job.UID) != hr.Status.JobUID {
		r.finish(&hr, v1alpha1.HookRunFailed, fmt.Sprintf(
			"Job %s was replaced by another Job of that name; the hook is not run again", job.Name))
		return ctrl.Result{}, r.patch(ctx, base, &hr)
	}
	return r.observe(ctx, log, base, &hr, job)
}

// job returns the HookRun's Job, from the cache and, when the cache does not
// have it, from the API server.
func (r *Reconciler) job(ctx context.Context, hr *v1alpha1.HookRun) (*batchv1.Job, bool, error) {
	key := types.NamespacedName{Name: hr.Status.JobName, Namespace: hr.Namespace}
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

// deleting holds a deleted HookRun while its Job runs and its deadline is
// ahead, records the Job's result in its status (so the Graph's mirror can
// carry it to the step's status.hookRecords) and holds it
// recordGrace longer, then lets it go (garbage collection deletes the Job
// and its Pods).
func (r *Reconciler) deleting(ctx context.Context, log zerolog.Logger, hr *v1alpha1.HookRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(hr, Finalizer) {
		return ctrl.Result{}, nil
	}
	if !terminal(hr.Status.Phase) && hr.Status.JobUID != "" && !r.expired(hr) {
		job, found, err := r.job(ctx, hr)
		if err != nil {
			return ctrl.Result{}, err
		}
		if found && string(job.UID) == hr.Status.JobUID {
			if jobCondition(job, batchv1.JobComplete) == nil && jobCondition(job, batchv1.JobFailed) == nil {
				log.Info().Str("job", job.Name).Msg("HookRun deleted while its Job runs; holding it until the Job ends")
				return ctrl.Result{RequeueAfter: r.untilDeadline(hr)}, nil
			}
			// The Job ended: record its result before letting go.
			if _, err := r.observe(ctx, log, hr.DeepCopy(), hr, job); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: recordGrace}, nil
		}
	}
	if hr.Status.FinishedAt != nil {
		if left := hr.Status.FinishedAt.Add(recordGrace).Sub(r.now()); left > 0 {
			return ctrl.Result{RequeueAfter: left}, nil
		}
	}
	base := hr.DeepCopy()
	controllerutil.RemoveFinalizer(hr, Finalizer)
	if err := r.Patch(ctx, hr, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer of hookrun %s: %w", hr.Name, err)
	}
	return ctrl.Result{}, nil
}

// notGenuine returns why hr was not created by its Bundle's Graph, or "":
// kro stamps kro.run/node-id on what it applies, and the Graph renders
// kardinal.io/bundle-uid as the UID of the Bundle it belongs to.
func (r *Reconciler) notGenuine(ctx context.Context, hr *v1alpha1.HookRun) (string, error) {
	if _, ok := hr.Labels[graph.LabelKRONodeID]; !ok {
		return "no " + graph.LabelKRONodeID + " label: not applied by kro", nil
	}
	var b v1alpha1.Bundle
	err := r.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: hr.Spec.BundleName}, &b)
	if apierrors.IsNotFound(err) {
		return fmt.Sprintf("Bundle %s does not exist", hr.Spec.BundleName), nil
	}
	if err != nil {
		return "", fmt.Errorf("get bundle %s: %w", hr.Spec.BundleName, err)
	}
	if got := hr.Labels[graph.LabelBundleUID]; got == "" || got != string(b.UID) {
		return fmt.Sprintf("%s %q is not the UID of Bundle %s", graph.LabelBundleUID, got, b.Name), nil
	}
	return "", nil
}

// recordGrace is how long a deleted HookRun whose Job finished stays, with
// its result in its status, so the Graph's mirror copies the result onto the
// step (status.hookRecords) before the HookRun goes and the Graph applies it
// again.
const recordGrace = 30 * time.Second

// siblingRunning reports whether another HookRun of the same Bundle,
// environment and phase has a Job running, or is an older one about to
// create its Job. Hooks of one phase run one after
// another, so this happens only when the hook list changed mid-flight (a
// hook renamed or reordered): kro creates the new HookRun in the same walk
// that prunes the old one, before the old one is even deleted. The new run
// waits for it rather than overlap it.
func (r *Reconciler) siblingRunning(ctx context.Context, hr *v1alpha1.HookRun) (bool, string, error) {
	var list v1alpha1.HookRunList
	if err := r.List(ctx, &list, client.InNamespace(hr.Namespace), client.MatchingLabels{
		"kardinal.io/pipeline":    hr.Spec.PipelineName,
		"kardinal.io/bundle":      hr.Spec.BundleName,
		"kardinal.io/environment": hr.Spec.Environment,
		graph.LabelHookPhase:      hr.Spec.Phase,
	}); err != nil {
		return false, "", fmt.Errorf("list hookruns: %w", err)
	}
	for _, other := range list.Items {
		if other.Name == hr.Name || other.Labels[graph.LabelBundleUID] != hr.Labels[graph.LabelBundleUID] {
			continue
		}
		if _, ok := other.Labels[graph.LabelKRONodeID]; !ok {
			continue // not applied by kro: it never runs, so it does not hold this one
		}
		if r.expired(&other) {
			continue
		}
		switch other.Status.Phase {
		case v1alpha1.HookRunRunning:
			return true, other.Name, nil
		case "", v1alpha1.HookRunPending:
			// A sibling that has not created its Job yet: the older one goes
			// first, so two Pending runs never both start (and never wait for
			// each other). A deleted one without a Job never starts.
			if other.DeletionTimestamp.IsZero() && older(&other, hr) {
				return true, other.Name, nil
			}
		}
	}
	return false, "", nil
}

// older reports whether a was created before b (by name when in the same
// second).
func older(a, b *v1alpha1.HookRun) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// start validates the HookRun, adds the finalizer and records when it
// started. The Job is created on the next reconcile, so a crash in between
// leaves a HookRun that knows its deadline.
func (r *Reconciler) start(ctx context.Context, log zerolog.Logger, base, hr *v1alpha1.HookRun, hash string) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	if rec := hr.Spec.Recorded; rec.Result != "" && rec.SpecHash == hash {
		// This hook already ran for the step (a HookRun of it was deleted
		// and the Graph applied it again): take the step's record, never run
		// the Job a second time.
		hr.Status.SpecHash, hr.Status.StartedAt = hash, &now
		switch rec.Result {
		case v1alpha1.HookRunSucceeded, v1alpha1.HookRunFailed:
			r.finish(hr, rec.Result, fmt.Sprintf("not run again: this hook already ran for the step (%s, recorded on its PromotionStep): %s",
				rec.Result, rec.Message))
		default:
			r.finish(hr, v1alpha1.HookRunFailed, "not run again: an earlier HookRun of this hook was deleted while its Job ran, "+
				"so its result is unknown; the hook is not run a second time")
		}
		log.Info().Str("recorded", rec.Result).Msg("hook already ran for this step; not run again")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	if hr.Spec.StepAdvanced {
		hr.Status.SpecHash, hr.Status.StartedAt = hash, &now
		when := "the step had already started"
		if hr.Spec.Phase == v1alpha1.HookPhasePost {
			when = "the step had already finished"
		}
		r.finish(hr, v1alpha1.HookRunSkipped, fmt.Sprintf("not run: the hook was added to the Pipeline after %s", when))
		log.Info().Msg("hook skipped: added after its step advanced")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	timeout, err := graph.HookTimeout(hr.Spec.Timeout)
	if err == nil {
		_, err = r.jobFor(hr, timeout)
	}
	if err != nil {
		hr.Status.SpecHash = hash
		hr.Status.StartedAt = &now
		r.finish(hr, v1alpha1.HookRunFailed, err.Error())
		log.Warn().Err(err).Msg("hook rejected")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	if !controllerutil.ContainsFinalizer(hr, Finalizer) {
		before := hr.DeepCopy()
		controllerutil.AddFinalizer(hr, Finalizer)
		if err := r.Patch(ctx, hr, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer to hookrun %s: %w", hr.Name, err)
		}
		// The patch returned the new resourceVersion; the status patch below
		// is locked on it, from the status as it was read.
		base = hr.DeepCopy()
		base.Status = *before.Status.DeepCopy()
	}
	deadline := metav1.NewTime(now.Add(timeout))
	hr.Status.SpecHash = hash
	hr.Status.StartedAt = &now
	hr.Status.Deadline = &deadline
	hr.Status.JobName = hr.Name
	hr.Status.Phase = v1alpha1.HookRunPending
	hr.Status.Message = "creating Job " + hr.Name
	if err := r.patch(ctx, base, hr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// createJob creates the hook's Job, owned by the HookRun. It is built from
// the spec recorded at start: a spec that changed since fails the HookRun.
func (r *Reconciler) createJob(ctx context.Context, log zerolog.Logger, base, hr *v1alpha1.HookRun) (ctrl.Result, error) {
	if r.expired(hr) {
		r.finish(hr, v1alpha1.HookRunFailed, "timed out before its Job was created")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	if meta.IsStatusConditionTrue(hr.Status.Conditions, ConditionSpecChangedAfterStart) {
		// The spec it started with is gone; running a different one would be
		// a second, unreviewed hook.
		r.finish(hr, v1alpha1.HookRunFailed, "spec.job changed after the HookRun started and before its Job was created")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	timeout, err := graph.HookTimeout(hr.Spec.Timeout)
	if err != nil {
		r.finish(hr, v1alpha1.HookRunFailed, err.Error())
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	spec, err := r.jobFor(hr, timeout)
	if err != nil {
		r.finish(hr, v1alpha1.HookRunFailed, err.Error())
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hr.Status.JobName,
			Namespace: hr.Namespace,
			Labels: map[string]string{
				LabelHookRun:              hr.Name,
				"kardinal.io/pipeline":    hr.Spec.PipelineName,
				"kardinal.io/bundle":      hr.Spec.BundleName,
				"kardinal.io/environment": hr.Spec.Environment,
				graph.LabelHookPhase:      hr.Spec.Phase,
			},
		},
		Spec: *spec,
	}
	if err := controllerutil.SetControllerReference(hr, job, r.Scheme()); err != nil {
		return ctrl.Result{}, fmt.Errorf("set owner of job %s: %w", job.Name, err)
	}
	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The cache holds only kardinal's Jobs: read the existing one
			// from the API server, so a foreign Job of the same name fails
			// the HookRun instead of looping on AlreadyExists.
			var existing batchv1.Job
			getErr := r.reader().Get(ctx, client.ObjectKeyFromObject(job), &existing)
			if apierrors.IsNotFound(getErr) {
				return ctrl.Result{RequeueAfter: time.Second}, nil // deleted meanwhile: try again
			}
			if getErr != nil {
				return ctrl.Result{}, fmt.Errorf("get existing job %s: %w", job.Name, getErr)
			}
			return r.adopt(ctx, base, hr, &existing)
		}
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			r.finish(hr, v1alpha1.HookRunFailed, fmt.Sprintf("create Job %s: %v", job.Name, err))
			return ctrl.Result{}, r.patch(ctx, base, hr)
		}
		return ctrl.Result{}, fmt.Errorf("create job %s: %w", job.Name, err)
	}
	log.Info().Str("job", job.Name).Str("phase", hr.Spec.Phase).Msg("hook Job created")
	hr.Status.JobUID = string(job.UID)
	hr.Status.Phase = v1alpha1.HookRunRunning
	hr.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, hr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.untilDeadline(hr)}, nil
}

// adopt records an existing Job named after the HookRun as its own when the
// HookRun is its controller owner. A Job of that name owned by anything else
// fails the HookRun: it is not this hook's run.
func (r *Reconciler) adopt(ctx context.Context, base, hr *v1alpha1.HookRun, job *batchv1.Job) (ctrl.Result, error) {
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.UID != hr.UID {
		r.finish(hr, v1alpha1.HookRunFailed, fmt.Sprintf(
			"a Job named %s exists and is not owned by this HookRun; delete or rename it", job.Name))
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	hr.Status.JobUID = string(job.UID)
	hr.Status.Phase = v1alpha1.HookRunRunning
	hr.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, hr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// observe copies the Job's outcome into the HookRun and enforces the
// deadline.
func (r *Reconciler) observe(ctx context.Context, log zerolog.Logger, base, hr *v1alpha1.HookRun, job *batchv1.Job) (ctrl.Result, error) {
	if c := jobCondition(job, batchv1.JobComplete); c != nil {
		r.finish(hr, v1alpha1.HookRunSucceeded, "Job "+job.Name+" completed")
		log.Info().Str("job", job.Name).Msg("hook succeeded")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	if c := jobCondition(job, batchv1.JobFailed); c != nil {
		msg := fmt.Sprintf("Job %s failed: %s", job.Name, c.Reason)
		if c.Message != "" {
			msg += ": " + c.Message
		}
		r.finish(hr, v1alpha1.HookRunFailed, msg)
		log.Info().Str("job", job.Name).Str("reason", c.Reason).Msg("hook failed")
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	if r.expired(hr) {
		policy := metav1.DeletePropagationBackground
		if err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete timed-out job %s: %w", job.Name, err)
		}
		r.finish(hr, v1alpha1.HookRunFailed, fmt.Sprintf("Job %s did not finish before the %s timeout; it was deleted",
			job.Name, hr.Status.Deadline.Sub(hr.Status.StartedAt.Time)))
		return ctrl.Result{}, r.patch(ctx, base, hr)
	}
	hr.Status.Phase = v1alpha1.HookRunRunning
	// No Pod counts in the message: every status write re-walks the Graph
	// (the mirror reads the HookRun), so it changes only with the phase.
	hr.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, hr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.untilDeadline(hr)}, nil
}

// jobFor returns the JobSpec to create for hr: spec.job checked against the
// security rules, with restartPolicy Never, backoffLimit 0 and
// activeDeadlineSeconds the timeout when they are unset, and
// ttlSecondsAfterFinished dropped (a Job deleted as it finishes would read
// as deleted before it finished; the HookRun owns its cleanup).
func (r *Reconciler) jobFor(hr *v1alpha1.HookRun, timeout time.Duration) (*batchv1.JobSpec, error) {
	if r.ControllerNamespace != "" && hr.Namespace == r.ControllerNamespace {
		return nil, fmt.Errorf("hooks may not run in the controller's namespace %s", hr.Namespace)
	}
	spec, err := graph.DecodeHookJob(hr.Spec.Job.Raw)
	if err != nil {
		return nil, err
	}
	pod := &spec.Template.Spec
	sa := pod.ServiceAccountName
	if sa == "" {
		sa = pod.DeprecatedServiceAccount //nolint:staticcheck // SA1019: the API server still honours it
	}
	if sa == "" {
		sa = DefaultServiceAccount
	}
	if r.GraphServiceAccount != "" && sa == r.GraphServiceAccount {
		return nil, fmt.Errorf("the hook's Pod may not run as the Graph ServiceAccount %q", sa)
	}
	allowed := r.AllowedServiceAccounts
	if len(allowed) == 0 {
		allowed = []string{DefaultServiceAccount}
	}
	if !slices.Contains(allowed, sa) {
		return nil, fmt.Errorf("the hook's Pod ServiceAccount %q is not in the controller's --hook-service-accounts (%v)", sa, allowed)
	}
	if spec.ManualSelector != nil && *spec.ManualSelector || spec.Selector != nil {
		return nil, fmt.Errorf("the hook's Job may not set selector or manualSelector")
	}
	if why := podSecurityViolation(r.PodSecurityLevel, &spec.Template.ObjectMeta, pod); why != "" {
		return nil, fmt.Errorf("the hook's Pod %s; the controller's --hook-pod-security-level refuses it", why)
	}
	if pod.RestartPolicy == "" {
		pod.RestartPolicy = corev1.RestartPolicyNever
	}
	if spec.BackoffLimit == nil {
		zero := int32(0)
		spec.BackoffLimit = &zero
	}
	if spec.ActiveDeadlineSeconds == nil {
		secs := int64(timeout.Seconds())
		spec.ActiveDeadlineSeconds = &secs
	}
	spec.TTLSecondsAfterFinished = nil
	if spec.Template.Labels == nil {
		spec.Template.Labels = map[string]string{}
	}
	spec.Template.Labels[LabelHookRun] = hr.Name
	return spec, nil
}

// markSpecChange keeps ConditionSpecChangedAfterStart current.
func (r *Reconciler) markSpecChange(hr *v1alpha1.HookRun, hash string) {
	if hash == hr.Status.SpecHash {
		return
	}
	meta.SetStatusCondition(&hr.Status.Conditions, metav1.Condition{
		Type:               ConditionSpecChangedAfterStart,
		Status:             metav1.ConditionTrue,
		Reason:             "SpecChanged",
		Message:            "spec.job or spec.timeout changed after the HookRun started; the change is not applied",
		ObservedGeneration: hr.Generation,
		LastTransitionTime: metav1.NewTime(r.now()),
	})
}

// finish moves hr to a terminal phase once; a terminal phase is never
// overwritten.
func (r *Reconciler) finish(hr *v1alpha1.HookRun, phase, msg string) {
	if terminal(hr.Status.Phase) {
		return
	}
	now := metav1.NewTime(r.now())
	hr.Status.Phase = phase
	hr.Status.Message = msg
	hr.Status.FinishedAt = &now
}

func (r *Reconciler) expired(hr *v1alpha1.HookRun) bool {
	return hr.Status.Deadline != nil && !r.now().Before(hr.Status.Deadline.Time)
}

func (r *Reconciler) untilDeadline(hr *v1alpha1.HookRun) time.Duration {
	if hr.Status.Deadline == nil {
		return requeueRunning
	}
	d := hr.Status.Deadline.Sub(r.now())
	if d <= 0 {
		return time.Second
	}
	if d > requeueRunning {
		return requeueRunning
	}
	return d
}

// patch writes hr's status when it differs from base, optimistic-locked on
// base's resourceVersion: a reconcile that read a stale copy gets a
// conflict (and is retried) instead of overwriting a newer status.
func (r *Reconciler) patch(ctx context.Context, base, hr *v1alpha1.HookRun) error {
	if equalStatus(&base.Status, &hr.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, hr, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch hookrun %s status: %w", hr.Name, err)
	}
	return nil
}

func equalStatus(a, b *v1alpha1.HookRunStatus) bool {
	return a.Phase == b.Phase && a.Message == b.Message && a.JobName == b.JobName &&
		a.JobUID == b.JobUID && a.SpecHash == b.SpecHash &&
		a.StartedAt.Equal(b.StartedAt) && a.Deadline.Equal(b.Deadline) && a.FinishedAt.Equal(b.FinishedAt) &&
		conditionsEqual(a.Conditions, b.Conditions)
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Status != b[i].Status || a[i].Reason != b[i].Reason ||
			a[i].Message != b[i].Message || a[i].ObservedGeneration != b[i].ObservedGeneration {
			return false
		}
	}
	return true
}

func terminal(phase string) bool {
	return phase == v1alpha1.HookRunSucceeded || phase == v1alpha1.HookRunFailed || phase == v1alpha1.HookRunSkipped
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == t && job.Status.Conditions[i].Status == corev1.ConditionTrue {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

// specHash is a hash of what the Job is built from (not spec.stepAdvanced,
// which follows the step). The job is hashed in a canonical form (sorted
// keys, no spaces): the API server and kro may re-encode the same JSON
// differently.
func specHash(spec *v1alpha1.HookRunSpec) string {
	h := sha256.New()
	job := spec.Job.Raw
	var v interface{}
	if err := json.Unmarshal(job, &v); err == nil {
		if canonical, err := json.Marshal(v); err == nil {
			job = canonical
		}
	}
	h.Write(job)
	h.Write([]byte{0})
	h.Write([]byte(spec.Timeout))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// SetupWithManager registers the reconciler; Job events wake their owner.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Sharded like every namespaced reconciler (pkg/shard): only the shard
	// that owns the namespace runs a hook's Job.
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.HookRun{}).
		Owns(&batchv1.Job{})
	return shard.Active().Complete(b, tracing.WrapReconciler("hookrun", r), &v1alpha1.HookRunList{})
}
