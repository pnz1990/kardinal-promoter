// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package renderrun implements the RenderRun reconciler: it renders one
// layout: branch environment for one Bundle in a sandboxed Job and writes the
// result to RenderRun.status. The Bundle's Graph creates the RenderRun once
// the environment's step asks for it and mirrors its status onto the
// PromotionStep (spec.live.renders); the PromotionStep reconciler reads only
// that copy. Rendering (kustomize build, helm template, and the git clone,
// commit and push around them) never runs in the controller: it runs in the
// kardinal-render image, in the Pipeline's namespace, with the Pipeline's
// own git Secret, no Kubernetes credentials, a read-only root filesystem, no
// capabilities, and memory, CPU and time limits.
package renderrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/renderjob"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	stepsimpl "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

func ptrTo[T any](v T) *T { return &v }

const (
	// LabelRenderRun is set on the Job and its Pod: the RenderRun name.
	LabelRenderRun = "kardinal.io/renderrun"
	// LabelRunJob marks the Jobs the controller runs (HookRuns and
	// RenderRuns); the informer caches only those.
	LabelRunJob = "kardinal.io/run-job"
	// LabelComponent is "render" on the render Pods, for NetworkPolicies.
	LabelComponent = "kardinal.io/component"

	// DefaultServiceAccount is the ServiceAccount the render Pods run as. The
	// reconciler creates it in a namespace that has none, without any role
	// and without a token: the Pod mounts none.
	DefaultServiceAccount = "kardinal-render"
	// DefaultTimeout bounds a render Job.
	DefaultTimeout = 5 * time.Minute
	// knownDigests is how many earlier renders' marker digests a render
	// accepts on the rendered branch (and a rollback trusts): as many as a
	// Pipeline keeps Bundles by default (historyLimit).
	knownDigests = 50
	// deadlineGrace is how much longer than the Job's activeDeadlineSeconds
	// the reconciler waits for the Job, from its creation, before it gives up
	// on its result: Kubernetes ends the Job first.
	deadlineGrace = 2 * time.Minute
	// backoffLimit retries a render whose Pod failed for a reason that is not
	// the render's (a node lost, a git server that hung up); a permanent
	// render error fails the Job at once (exit code ExitPermanent).
	backoffLimit = 3

	requeueRunning = 30 * time.Second
)

// DefaultResources are the render container's requests and limits.
func DefaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")},
	}
}

// Reconciler runs RenderRun Jobs.
type Reconciler struct {
	client.Client
	// APIReader reads from the API server, uncached: Jobs the cache has not
	// seen yet, and the render Pods (not cached at all). Nil means Client.
	APIReader client.Reader
	// Image is the kardinal-render image.
	Image string
	// ImagePullPolicy of the render container; empty means the default.
	ImagePullPolicy corev1.PullPolicy
	// ServiceAccount the render Pods run as; empty means DefaultServiceAccount.
	ServiceAccount string
	// Resources of the render container; zero means DefaultResources.
	Resources corev1.ResourceRequirements
	// Timeout bounds a render Job; zero means DefaultTimeout.
	Timeout time.Duration
	// ControllerNamespace is the controller's own namespace: a render there
	// is refused, it would run next to the controller's credentials.
	ControllerNamespace string
	// AuthorName and AuthorEmail sign the rendered commits.
	AuthorName, AuthorEmail string
	// ImagePullSecrets are added to the render Pods (they must exist in the
	// Pipeline namespace).
	ImagePullSecrets []string
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

func (r *Reconciler) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}

func (r *Reconciler) serviceAccount() string {
	if r.ServiceAccount != "" {
		return r.ServiceAccount
	}
	return DefaultServiceAccount
}

var renderRunsResource = v1alpha1.GroupVersion.WithResource("renderruns").GroupResource()

// Reconcile runs one RenderRun forward. It is idempotent: each step finds
// what an earlier, possibly interrupted, reconcile did (the Job by its
// deterministic name and owner) before it acts, and every status write is
// optimistic-locked.
//
//	no status.specHash        → record specHash, the known marker digests, startedAt, deadline
//	no Job, no status.jobUID  → create the ServiceAccount if missing, then the Job (or adopt ours)
//	Job gone or replaced      → Failed (never re-run)
//	Job Complete / Failed     → Succeeded with the Pod's result / Failed with why
//	deadline passed           → Failed, Job deleted with its Pod
//	terminal                  → nothing more
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, renderRunsResource, r.reconcile)
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("renderrun", req.Name).Str("namespace", req.Namespace).Logger()
	var run v1alpha1.RenderRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get renderrun %s: %w", req.Name, err)
	}
	if terminal(run.Status.Phase) || !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := run.DeepCopy()
	if run.Status.SpecHash == "" {
		return r.start(ctx, base, &run)
	}
	job, found, err := r.job(ctx, &run)
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case !found && run.Status.JobUID == "":
		return r.createJob(ctx, log, base, &run)
	case !found:
		r.finish(&run, v1alpha1.RenderRunFailed, fmt.Sprintf("Job %s was deleted before it finished", run.Status.JobName), nil)
		return ctrl.Result{}, r.patch(ctx, base, &run)
	}
	if run.Status.JobUID == "" {
		return r.adopt(ctx, base, &run, job)
	}
	if string(job.UID) != run.Status.JobUID {
		r.finish(&run, v1alpha1.RenderRunFailed, fmt.Sprintf("Job %s was replaced by another Job of that name", job.Name), nil)
		return ctrl.Result{}, r.patch(ctx, base, &run)
	}
	return r.observe(ctx, log, base, &run, job)
}

// start records what the render runs with: the spec hash, the marker
// digests of the earlier renders of this environment, and the deadline. The
// Job is created on the next reconcile.
func (r *Reconciler) start(ctx context.Context, base, run *v1alpha1.RenderRun) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	run.Status.SpecHash = specHash(&run.Spec)
	run.Status.StartedAt = &now
	if r.Image == "" {
		r.finish(run, v1alpha1.RenderRunFailed, "the controller has no render image (--render-image, Helm render.image), "+
			"so layout: branch cannot render", nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if r.ControllerNamespace != "" && run.Namespace == r.ControllerNamespace {
		r.finish(run, v1alpha1.RenderRunFailed, fmt.Sprintf("renders may not run in the controller's namespace %s", run.Namespace), nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	known, unconfirmed, err := r.earlierRenders(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Until its Job exists, a RenderRun waits at most the timeout; the Job
	// gets its own deadline when it is created (createJob).
	deadline := metav1.NewTime(now.Add(r.timeout()))
	run.Status.KnownMarkerDigests = known
	run.Status.UnconfirmedBundles = unconfirmed
	run.Status.Deadline = &deadline
	run.Status.JobName = run.Name
	run.Status.Phase = v1alpha1.RenderRunPending
	run.Status.Message = "creating Job " + run.Name
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// earlierRenders reads the earlier RenderRuns of the same Pipeline
// environment and rendered branch. known are the marker digests of the
// newest Succeeded ones: the rendered branch's marker must be one of them (a
// push that rewrites the files and the marker together is drift too).
// unconfirmed is the Bundle of the newest one, when it Failed after the
// newest Succeeded one: its Job may have pushed and then lost its result
// (its Pod deleted, the controller past its deadline), so the branch's
// marker may name it. Older lost renders are not adopted: the newest one ran
// after them on the same branch. None of either means the first render, or an
// environment whose earlier RenderRuns are gone: the branch's own marker is
// used.
func (r *Reconciler) earlierRenders(ctx context.Context, run *v1alpha1.RenderRun) (known, unconfirmed []string, err error) {
	var list v1alpha1.RenderRunList
	if err := r.List(ctx, &list, client.InNamespace(run.Namespace), client.MatchingLabels{
		"kardinal.io/pipeline": run.Spec.PipelineName, "kardinal.io/environment": run.Spec.Environment,
	}); err != nil {
		return nil, nil, fmt.Errorf("list renderruns: %w", err)
	}
	done := make([]v1alpha1.RenderRun, 0, len(list.Items))
	for _, o := range list.Items {
		if o.Name != run.Name && terminal(o.Status.Phase) && o.Status.FinishedAt != nil &&
			o.Spec.Git.RenderedBranch == run.Spec.Git.RenderedBranch {
			done = append(done, o)
		}
	}
	// Every earlier render, newest first: the RenderRuns that remain, and
	// the steps of retired Bundles, whose Graph was deleted with its
	// RenderRuns (status.retiredSteps keeps a render's marker digest).
	var renders []earlierRender
	for _, o := range done {
		rd := earlierRender{at: o.Status.FinishedAt.Time, bundle: o.Spec.BundleName,
			failed: o.Status.Phase == v1alpha1.RenderRunFailed}
		if res := o.Status.Result; !rd.failed && res != nil {
			rd.digest = res.MarkerDigest
		}
		renders = append(renders, rd)
	}
	retired, err := r.retiredRenders(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	renders = append(renders, retired...)
	sort.SliceStable(renders, func(i, j int) bool { return renders[j].at.Before(renders[i].at) })
	seen := map[string]bool{}
	confirmed := false
	for _, rd := range renders {
		if rd.failed {
			// Only the newest failed render after the last accepted one:
			// its Job may have pushed before its result was lost; an older
			// one's push was overwritten by the newer, or is not the head.
			if !confirmed && len(unconfirmed) == 0 {
				unconfirmed = append(unconfirmed, rd.bundle)
			}
			continue
		}
		confirmed = true
		if rd.digest != "" && !seen[rd.digest] && len(known) < knownDigests {
			seen[rd.digest] = true
			known = append(known, rd.digest)
		}
	}
	return known, unconfirmed, nil
}

// earlierRender is one earlier render of an environment.
type earlierRender struct {
	at     time.Time
	digest string // the marker digest of a render kardinal accepted
	bundle string
	failed bool // the render failed: its result may have been lost
}

// retiredRenders returns the renders of the environment that the
// Pipeline's retired Bundles keep in status.retiredSteps: a step with a
// marker digest got past its render; a Failed one that asked for its render
// and has none may have lost its render's result.
func (r *Reconciler) retiredRenders(ctx context.Context, run *v1alpha1.RenderRun) ([]earlierRender, error) {
	var bundles v1alpha1.BundleList
	if err := r.List(ctx, &bundles, client.InNamespace(run.Namespace)); err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	var out []earlierRender
	for i := range bundles.Items {
		b := &bundles.Items[i]
		if b.Spec.Pipeline != run.Spec.PipelineName || b.Name == run.Spec.BundleName || !lifecycle.Retired(b) {
			continue
		}
		for _, rs := range b.Status.RetiredSteps {
			if rs.Environment != run.Spec.Environment {
				continue
			}
			switch {
			case rs.MarkerDigest != "":
				at := rs.CreatedAt.Time
				if rs.VerifiedAt != nil {
					at = rs.VerifiedAt.Time
				}
				out = append(out, earlierRender{at: at, digest: rs.MarkerDigest, bundle: b.Name})
			case rs.State == "Failed" && rs.RenderRequested:
				out = append(out, earlierRender{at: rs.CreatedAt.Time, bundle: b.Name, failed: true})
			}
		}
	}
	return out, nil
}

func (r *Reconciler) job(ctx context.Context, run *v1alpha1.RenderRun) (*batchv1.Job, bool, error) {
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

// ensureServiceAccount creates the render ServiceAccount in the namespace
// when it is missing: no role binds it, and it does not mount a token.
func (r *Reconciler) ensureServiceAccount(ctx context.Context, ns string) error {
	var sa corev1.ServiceAccount
	key := types.NamespacedName{Name: r.serviceAccount(), Namespace: ns}
	err := r.reader().Get(ctx, key, &sa)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get serviceaccount %s: %w", key, err)
	}
	no := false
	sa = corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: ns, Labels: map[string]string{
			"app.kubernetes.io/managed-by": "kardinal-promoter", LabelComponent: "render"}},
		AutomountServiceAccountToken: &no,
	}
	if err := r.Create(ctx, &sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create serviceaccount %s: %w", key, err)
	}
	return nil
}

// JobSpec is the render Job's spec for run.
func (r *Reconciler) JobSpec(run *v1alpha1.RenderRun) (*batchv1.JobSpec, error) {
	cfg := renderjob.ConfigFromRun(run, r.AuthorName, r.AuthorEmail)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode render config: %w", err)
	}
	res := r.Resources
	if len(res.Limits) == 0 && len(res.Requests) == 0 {
		res = DefaultResources()
	}
	yes, no := true, false
	uid := int64(65532)
	retries := int32(backoffLimit)
	secs := int64(r.timeout().Seconds())
	workSize := resource.MustParse("1Gi")
	tmpSize := resource.MustParse("64Mi")
	labels := map[string]string{LabelRenderRun: run.Name, LabelRunJob: "renderrun", LabelComponent: "render",
		"kardinal.io/pipeline": run.Spec.PipelineName, "kardinal.io/environment": run.Spec.Environment}
	container := corev1.Container{
		Name:            "render",
		Image:           r.Image,
		ImagePullPolicy: r.ImagePullPolicy,
		Env:             []corev1.EnvVar{{Name: renderjob.ConfigEnv, Value: string(raw)}},
		Resources:       res,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "work", MountPath: renderjob.WorkDir},
			{Name: "tmp", MountPath: "/tmp"},
		},
		TerminationMessagePath:   renderjob.TerminationMessagePath,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &yes,
			RunAsUser:                &uid,
			RunAsGroup:               &uid,
			ReadOnlyRootFilesystem:   &yes,
			AllowPrivilegeEscalation: &no,
			Privileged:               &no,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
	volumes := []corev1.Volume{
		{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &workSize}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpSize}}},
	}
	if name := run.Spec.Git.SecretName; name != "" {
		mode := int32(0o440)
		volumes = append(volumes, corev1.Volume{Name: "git", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: name, DefaultMode: &mode, Items: gitSecretItems(run.Spec.Git.URL)}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "git", MountPath: renderjob.GitSecretDir, ReadOnly: true})
	}
	var pullSecrets []corev1.LocalObjectReference
	for _, n := range r.ImagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: n})
	}
	return &batchv1.JobSpec{
		BackoffLimit:          &retries,
		ActiveDeadlineSeconds: &secs,
		// A Pod evicted or preempted (DisruptionTarget) is not a render
		// failure and does not count; a render that failed for good (exit
		// renderjob.ExitPermanent) fails the Job at once; other failures are
		// retried up to backoffLimit.
		PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{
			{Action: batchv1.PodFailurePolicyActionIgnore, OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
				{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}}},
			{Action: batchv1.PodFailurePolicyActionFailJob, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
				ContainerName: ptrTo("render"), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn, Values: []int32{renderjob.ExitPermanent}}},
		}},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				ServiceAccountName:           r.serviceAccount(),
				AutomountServiceAccountToken: &no,
				EnableServiceLinks:           &no,
				RestartPolicy:                corev1.RestartPolicyNever,
				SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Containers:       []corev1.Container{container},
				Volumes:          volumes,
				ImagePullSecrets: pullSecrets,
			},
		},
	}, nil
}

func (r *Reconciler) createJob(ctx context.Context, log zerolog.Logger, base, run *v1alpha1.RenderRun) (ctrl.Result, error) {
	if r.expired(run) {
		r.finish(run, v1alpha1.RenderRunFailed, "timed out before its Job was created", nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if specHash(&run.Spec) != run.Status.SpecHash {
		r.finish(run, v1alpha1.RenderRunFailed, "the RenderRun's spec changed before its Job was created", nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if err := r.ensureServiceAccount(ctx, run.Namespace); err != nil {
		return ctrl.Result{}, err
	}
	spec, err := r.JobSpec(run)
	if err != nil {
		r.finish(run, v1alpha1.RenderRunFailed, err.Error(), nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: run.Status.JobName, Namespace: run.Namespace,
			Labels: spec.Template.Labels},
		Spec: *spec,
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
			r.finish(run, v1alpha1.RenderRunFailed, fmt.Sprintf("create Job %s: %v", job.Name, err), nil)
			return ctrl.Result{}, r.patch(ctx, base, run)
		}
		return ctrl.Result{}, fmt.Errorf("create job %s: %w", job.Name, err)
	}
	log.Info().Str("job", job.Name).Msg("render Job created")
	// The Job's own deadline (activeDeadlineSeconds) runs from its start;
	// the reconciler waits longer, from the creation, so Kubernetes ends a
	// slow Job first and its Pod's result is not lost to a race.
	deadline := metav1.NewTime(r.now().Add(r.timeout() + deadlineGrace))
	run.Status.Deadline = &deadline
	run.Status.JobUID = string(job.UID)
	run.Status.Phase = v1alpha1.RenderRunRunning
	run.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.untilDeadline(run)}, nil
}

// adopt records an existing Job named after the RenderRun as its own when
// the RenderRun is its controller owner; any other Job of that name fails it.
func (r *Reconciler) adopt(ctx context.Context, base, run *v1alpha1.RenderRun, job *batchv1.Job) (ctrl.Result, error) {
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.UID != run.UID {
		r.finish(run, v1alpha1.RenderRunFailed, fmt.Sprintf(
			"a Job named %s exists and is not owned by this RenderRun; delete or rename it", job.Name), nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	run.Status.JobUID = string(job.UID)
	run.Status.Phase = v1alpha1.RenderRunRunning
	run.Status.Message = "Job " + job.Name + " running"
	if err := r.patch(ctx, base, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// observe takes the Job's outcome: the result the render Pod wrote to its
// termination message, or why the Pod ended without one (out of memory,
// past its deadline).
func (r *Reconciler) observe(ctx context.Context, log zerolog.Logger, base, run *v1alpha1.RenderRun, job *batchv1.Job) (ctrl.Result, error) {
	complete, failed := jobCondition(job, batchv1.JobComplete), jobCondition(job, batchv1.JobFailed)
	if complete == nil && failed == nil {
		// Running: say why the Pod is not running yet, if it is stuck.
		pods, err := r.jobPods(ctx, job)
		if err != nil {
			return ctrl.Result{}, err
		}
		msg := "Job " + job.Name + " running"
		if why := waitingReason(pods); why != "" {
			msg += "; " + why
		}
		run.Status.Message = msg
	}
	if complete != nil || failed != nil {
		res, why, err := r.podResult(ctx, job, complete != nil)
		if err != nil {
			return ctrl.Result{}, err
		}
		if complete != nil && why == "" && res.Error == "" {
			if invalid := validResult(run, &res.RenderRunResult); invalid != "" {
				why = "the render Job reported a result that does not hold: " + invalid
			}
		}
		switch {
		case complete != nil && why == "" && res.Error == "":
			result := res.RenderRunResult
			r.finish(run, v1alpha1.RenderRunSucceeded, renderedMessage(&result), &result)
			log.Info().Str("job", job.Name).Str("commit", result.CommitSHA).Int("objects", result.Objects).Msg("render succeeded")
		case res.Error != "":
			r.finish(run, v1alpha1.RenderRunFailed, res.Error, nil)
		case why != "":
			r.finish(run, v1alpha1.RenderRunFailed, why, nil)
		default:
			msg := fmt.Sprintf("Job %s failed: %s", job.Name, failed.Reason)
			if failed.Reason == "DeadlineExceeded" {
				msg = fmt.Sprintf("the render did not finish within %s", r.timeout())
			} else if failed.Message != "" {
				msg += ": " + failed.Message
			}
			r.finish(run, v1alpha1.RenderRunFailed, msg, nil)
		}
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if r.expired(run) {
		policy := metav1.DeletePropagationBackground
		if err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete timed-out job %s: %w", job.Name, err)
		}
		r.finish(run, v1alpha1.RenderRunFailed, fmt.Sprintf("the render did not finish within %s; its Job was deleted", r.timeout()), nil)
		return ctrl.Result{}, r.patch(ctx, base, run)
	}
	if run.Status.Message != base.Status.Message {
		// Only when it changes: every write re-walks the Bundle's Graph.
		if err := r.patch(ctx, base, run); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: min(r.untilDeadline(run), 15*time.Second)}, nil
}

// jobPods lists the Pods of job: those whose controller is job (by UID) and
// whose name starts with the Job's, so a Pod someone else created with the
// Job's labels is never read.
func (r *Reconciler) jobPods(ctx context.Context, job *batchv1.Job) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.reader().List(ctx, &pods, client.InNamespace(job.Namespace),
		client.MatchingLabels{"batch.kubernetes.io/controller-uid": string(job.UID)}); err != nil {
		return nil, fmt.Errorf("list pods of job %s: %w", job.Name, err)
	}
	var out []corev1.Pod
	for _, p := range pods.Items {
		owner := metav1.GetControllerOf(&p)
		if owner != nil && owner.UID == job.UID && owner.Kind == "Job" && strings.HasPrefix(p.Name, job.Name+"-") {
			out = append(out, p)
		}
	}
	return out, nil
}

// podResult reads the render Pod's termination message: of the Pod that
// exited 0 when the Job completed, else of the Pod that terminated last.
// why says how the Pod ended when it wrote none (OOMKilled, an error before
// main ran), or that no Pod of the Job is left.
func (r *Reconciler) podResult(ctx context.Context, job *batchv1.Job, complete bool) (renderjob.Result, string, error) {
	pods, err := r.jobPods(ctx, job)
	if err != nil {
		return renderjob.Result{}, "", err
	}
	var best *corev1.ContainerStateTerminated
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			t := cs.State.Terminated
			if cs.Name != "render" || t == nil {
				continue
			}
			switch {
			case best == nil,
				complete && t.ExitCode == 0 && best.ExitCode != 0,
				(t.ExitCode == 0) == (best.ExitCode == 0) && best.FinishedAt.Before(&t.FinishedAt):
				best = t
			}
		}
	}
	if best == nil {
		return renderjob.Result{}, "the render Job finished but its Pod is gone, so its result is unknown", nil
	}
	if best.Reason == "OOMKilled" {
		limit := ""
		if m, ok := job.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]; ok {
			limit = " (" + m.String() + ")"
		}
		return renderjob.Result{}, "the render ran out of memory" + limit + " and was stopped", nil
	}
	res, err := renderjob.ParseMessage(best.Message)
	if err != nil {
		return renderjob.Result{}, fmt.Sprintf("the render Pod exited %d (%s): %v", best.ExitCode, best.Reason, err), nil
	}
	return res, "", nil
}

// waitingReasons are the container waiting reasons that mean the render Pod
// will not start by itself.
var waitingReasons = []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName", "CreateContainerConfigError",
	"CreateContainerError"}

// waitingReason says why a render Pod is not running: a container that
// cannot start (its image cannot be pulled) or a Pod that cannot be
// scheduled, or "".
func waitingReason(pods []corev1.Pod) string {
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && slices.Contains(waitingReasons, w.Reason) {
				return fmt.Sprintf("Pod %s: %s: %s", p.Name, w.Reason, truncate(w.Message, 300))
			}
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
				return fmt.Sprintf("Pod %s: Unschedulable: %s", p.Name, truncate(c.Message, 300))
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// sha1RE is a full git commit id.
var sha1RE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// validResult returns "" when a render result can be true for run, or what
// does not hold: a pushed render names a full commit, on the rendered branch
// (or the promotion branch of a pr-review step), rendered from a full DRY
// commit. The step then checks the branch head itself.
func validResult(run *v1alpha1.RenderRun, res *v1alpha1.RenderRunResult) string {
	if res.DryCommit != "" && !sha1RE.MatchString(res.DryCommit) {
		return fmt.Sprintf("dryCommit %q is not a commit id", res.DryCommit)
	}
	if res.NoChanges {
		// Nothing pushed: the commit is the rendered branch's head, which the
		// step checks on the remote.
		if res.Branch != "" {
			return "an unchanged render reports a pushed branch"
		}
		if !sha1RE.MatchString(res.CommitSHA) {
			return fmt.Sprintf("commitSHA %q (the rendered branch's head) is not a commit id", res.CommitSHA)
		}
		return ""
	}
	if !sha1RE.MatchString(res.CommitSHA) {
		return fmt.Sprintf("commitSHA %q is not a commit id", res.CommitSHA)
	}
	want := run.Spec.Git.RenderedBranch
	if run.Spec.Git.PullRequest {
		want = stepsimpl.PRBranch(run.Namespace, run.Spec.BundleName, run.Spec.Environment)
	}
	if res.Branch != want {
		return fmt.Sprintf("branch %q is not %q", res.Branch, want)
	}
	return ""
}

func renderedMessage(res *v1alpha1.RenderRunResult) string {
	if res.NoChanges {
		return fmt.Sprintf("nothing to commit: the rendered branch holds the render of %s", short(res.DryCommit))
	}
	return fmt.Sprintf("rendered %d objects from %s with %s and pushed %s to %s",
		res.Objects, short(res.DryCommit), res.Renderer, short(res.CommitSHA), res.Branch)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (r *Reconciler) finish(run *v1alpha1.RenderRun, phase, msg string, res *v1alpha1.RenderRunResult) {
	if terminal(run.Status.Phase) {
		return
	}
	now := metav1.NewTime(r.now())
	run.Status.Phase = phase
	run.Status.Message = msg
	run.Status.FinishedAt = &now
	run.Status.Result = res
}

func (r *Reconciler) expired(run *v1alpha1.RenderRun) bool {
	return run.Status.Deadline != nil && !r.now().Before(run.Status.Deadline.Time)
}

func (r *Reconciler) untilDeadline(run *v1alpha1.RenderRun) time.Duration {
	if run.Status.Deadline == nil {
		return requeueRunning
	}
	d := run.Status.Deadline.Sub(r.now())
	if d <= 0 {
		return time.Second
	}
	return min(d, requeueRunning)
}

// patch writes run's status, optimistic-locked on base's resourceVersion.
func (r *Reconciler) patch(ctx context.Context, base, run *v1alpha1.RenderRun) error {
	if err := r.Status().Patch(ctx, run, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch renderrun %s status: %w", run.Name, err)
	}
	return nil
}

func terminal(phase string) bool {
	return phase == v1alpha1.RenderRunSucceeded || phase == v1alpha1.RenderRunFailed
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == t && job.Status.Conditions[i].Status == corev1.ConditionTrue {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

// specHash is a hash of the RenderRun's spec in canonical JSON.
func specHash(spec *v1alpha1.RenderRunSpec) string {
	b, _ := json.Marshal(spec)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// SetupWithManager registers the reconciler; Job events wake their owner.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.RenderRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

// gitSecretItems are the keys of the git Secret the render Job mounts: the
// ones its spec.git.url authenticates with, as the controller reads them
// (sshPrivateKey and knownHosts for an ssh URL, token otherwise). A key the
// Secret lacks fails the Pod's volume mount, so the Job does not start;
// the controller's own steps say which key is missing first.
func gitSecretItems(url string) []corev1.KeyToPath {
	if scm.IsSSHRemote(url) {
		return []corev1.KeyToPath{{Key: "sshPrivateKey", Path: "sshPrivateKey"}, {Key: "knownHosts", Path: "knownHosts"}}
	}
	return []corev1.KeyToPath{{Key: "token", Path: "token"}}
}
