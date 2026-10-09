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
// and its Pods), a finished HookRun never creates a Job again, and a spec
// edit after the start is reported, not applied.
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
)

const (
	// ConditionSpecChangedAfterStart is True when spec.job or spec.timeout
	// changed after the Job was created. The change is not applied.
	ConditionSpecChangedAfterStart = "SpecChangedAfterStart"

	// LabelHookRun is set on the Job and its Pods: the HookRun name.
	LabelHookRun = "kardinal.io/hookrun"

	// DefaultServiceAccount is the only Pod ServiceAccount a hook may use
	// when the controller is given no allowlist.
	DefaultServiceAccount = "default"

	// requeueRunning is the fallback poll while the Job runs; the Job watch
	// normally wakes the HookRun first.
	requeueRunning = 30 * time.Second
)

// Reconciler runs HookRun Jobs.
type Reconciler struct {
	client.Client

	// AllowedServiceAccounts are the Pod ServiceAccount names a hook's Job
	// may run as (--hook-service-accounts). Empty means only "default".
	AllowedServiceAccounts []string

	// GraphServiceAccount is the ServiceAccount kro impersonates for Graphs.
	// A hook may never run as it, whatever the allowlist says: it can write
	// every object a Graph renders.
	GraphServiceAccount string

	// NowFn returns the current time; nil means time.Now.
	NowFn func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

var hookRunsResource = v1alpha1.GroupVersion.WithResource("hookruns").GroupResource()

// Reconcile runs one HookRun forward. It is idempotent: each step finds what
// an earlier, possibly interrupted, reconcile did (the Job by its
// deterministic name and owner) before it acts.
//
//	no status.specHash      → validate, record specHash, startedAt, deadline
//	no Job, no status.jobUID → create the Job (or adopt the one this HookRun owns)
//	Job gone or replaced    → Failed (never re-run)
//	Job Complete / Failed   → Succeeded / Failed
//	deadline passed         → Failed, Job deleted with its Pods
//	Succeeded / Failed      → nothing more (only SpecChangedAfterStart is kept current)
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
		return ctrl.Result{}, nil // garbage collection deletes the Job and its Pods
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

	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Name: hr.Status.JobName, Namespace: hr.Namespace}, &job)
	switch {
	case apierrors.IsNotFound(err) && hr.Status.JobUID == "":
		return r.createJob(ctx, log, base, &hr)
	case apierrors.IsNotFound(err):
		r.finish(&hr, v1alpha1.HookRunFailed, fmt.Sprintf(
			"Job %s was deleted before it finished; the hook is not run again", hr.Status.JobName))
		return ctrl.Result{}, r.patch(ctx, base, &hr)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get job %s: %w", hr.Status.JobName, err)
	}
	if hr.Status.JobUID == "" {
		// A crash between the create and the status write: adopt the Job if
		// it is this HookRun's.
		return r.adopt(ctx, base, &hr, &job)
	}
	if string(job.UID) != hr.Status.JobUID {
		r.finish(&hr, v1alpha1.HookRunFailed, fmt.Sprintf(
			"Job %s was replaced by another Job of that name; the hook is not run again", job.Name))
		return ctrl.Result{}, r.patch(ctx, base, &hr)
	}
	return r.observe(ctx, log, base, &hr, &job)
}

// start validates the HookRun and records when it started. The Job is
// created on the next reconcile, so a crash in between leaves a HookRun
// that knows its deadline.
func (r *Reconciler) start(ctx context.Context, log zerolog.Logger, base, hr *v1alpha1.HookRun, hash string) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
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
// the spec recorded at start; a spec that changed since is reported and the
// Job is built from the current spec only when the hash still matches.
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
			var existing batchv1.Job
			if getErr := r.Get(ctx, client.ObjectKeyFromObject(job), &existing); getErr != nil {
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
			"a Job named %s exists and is not owned by this HookRun", job.Name))
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

// jobFor returns the JobSpec to create for hr: spec.job with restartPolicy
// Never and activeDeadlineSeconds the timeout when unset, after checking the
// Pod's ServiceAccount against the allowlist.
func (r *Reconciler) jobFor(hr *v1alpha1.HookRun, timeout time.Duration) (*batchv1.JobSpec, error) {
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
	if pod.RestartPolicy == "" {
		pod.RestartPolicy = corev1.RestartPolicyNever
	}
	if spec.ActiveDeadlineSeconds == nil {
		secs := int64(timeout.Seconds())
		spec.ActiveDeadlineSeconds = &secs
	}
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

// patch writes hr's status when it differs from base.
func (r *Reconciler) patch(ctx context.Context, base, hr *v1alpha1.HookRun) error {
	if equalStatus(&base.Status, &hr.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, hr, client.MergeFrom(base)); err != nil {
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
	return phase == v1alpha1.HookRunSucceeded || phase == v1alpha1.HookRunFailed
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == t && job.Status.Conditions[i].Status == corev1.ConditionTrue {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

// specHash is a hash of what the Job is built from. The job is hashed in a
// canonical form (sorted keys, no spaces): the API server and kro may
// re-encode the same JSON differently.
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.HookRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
