// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderrun_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/renderrun"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/renderjob"
	stepsimpl "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func run(name string) *v1alpha1.RenderRun {
	return &v1alpha1.RenderRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", UID: types.UID("uid-" + name),
			Labels: map[string]string{"kardinal.io/pipeline": "web", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.RenderRunSpec{PipelineName: "web", BundleName: "web-v2", Environment: "prod", Path: "environments/prod",
			Git: v1alpha1.RenderRunGit{URL: "https://git.example.com/org/repo.git", SecretName: "git-creds",
				SourceBranch: "main", RenderedBranch: "env/prod"},
			Bundle: v1alpha1.RenderRunBundle{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "r/web", Tag: "2"}}}},
	}
}

type env struct {
	c   client.Client
	r   *renderrun.Reconciler
	now time.Time
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	e := &env{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	e.c = fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.RenderRun{}, &batchv1.Job{}).
		// The API server gives every object a UID; the fake client does not.
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetUID() == "" {
				obj.SetUID(types.UID("uid-" + obj.GetName()))
			}
			return c.Create(ctx, obj, opts...)
		}}).Build()
	e.r = &renderrun.Reconciler{Client: e.c, Image: "ghcr.io/x/render:v1", ControllerNamespace: "kardinal-system",
		ImagePullSecrets: []string{"regcred"},
		AuthorName:       "kardinal-promoter", AuthorEmail: "k@example.com", NowFn: func() time.Time { return e.now }}
	return e
}

func (e *env) reconcile(t *testing.T, name string) (ctrl.Result, *v1alpha1.RenderRun) {
	t.Helper()
	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team", Name: name}})
	require.NoError(t, err)
	var got v1alpha1.RenderRun
	require.NoError(t, e.c.Get(context.Background(), types.NamespacedName{Namespace: "team", Name: name}, &got))
	return res, &got
}

func (e *env) job(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	var job batchv1.Job
	require.NoError(t, e.c.Get(context.Background(), types.NamespacedName{Namespace: "team", Name: name}, &job))
	return &job
}

// finishJob marks the Job complete or failed and creates its Pod with a
// terminated render container.
func (e *env) finishJob(t *testing.T, job *batchv1.Job, complete bool, term corev1.ContainerStateTerminated) {
	t.Helper()
	ctx := context.Background()
	cond := batchv1.JobComplete
	if !complete {
		cond = batchv1.JobFailed
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
	require.NoError(t, e.c.Status().Update(ctx, job))
	yes := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x", Namespace: "team",
		Labels:          map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &yes}}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "render", Image: "i"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "render", State: corev1.ContainerState{Terminated: &term}}}}}
	require.NoError(t, e.c.Create(ctx, pod))
}

// TestRenderRun_Succeeds: the RenderRun records what it runs with, creates
// the render ServiceAccount and a sandboxed Job, and takes the result from
// the Pod's termination message.
func TestRenderRun_Succeeds(t *testing.T) {
	e := newEnv(t, run("rr"))
	res, got := e.reconcile(t, "rr")
	assert.Equal(t, "Pending", got.Status.Phase)
	assert.True(t, res.Requeue) //nolint:staticcheck // the reconciler uses Requeue
	require.NotNil(t, got.Status.Deadline)
	assert.Equal(t, 5*time.Minute, got.Status.Deadline.Sub(got.Status.StartedAt.Time))

	_, got = e.reconcile(t, "rr")
	assert.Equal(t, "Running", got.Status.Phase)
	var sa corev1.ServiceAccount
	require.NoError(t, e.c.Get(context.Background(), types.NamespacedName{Namespace: "team", Name: "kardinal-render"}, &sa))
	require.NotNil(t, sa.AutomountServiceAccountToken)
	assert.False(t, *sa.AutomountServiceAccountToken)

	job := e.job(t, "rr")
	assert.Equal(t, "renderrun", job.Labels["kardinal.io/run-job"])
	pod := job.Spec.Template.Spec
	assert.Equal(t, "kardinal-render", pod.ServiceAccountName)
	assert.False(t, *pod.AutomountServiceAccountToken, "no Kubernetes credentials")
	assert.False(t, *pod.EnableServiceLinks)
	assert.Equal(t, "render", job.Spec.Template.Labels["kardinal.io/component"], "the NetworkPolicy selects it")
	assert.Equal(t, int64(300), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int32(3), *job.Spec.BackoffLimit, "transient failures are retried")
	require.NotNil(t, job.Spec.PodFailurePolicy)
	rules := job.Spec.PodFailurePolicy.Rules
	require.Len(t, rules, 2)
	assert.Equal(t, batchv1.PodFailurePolicyActionIgnore, rules[0].Action, "an evicted Pod does not count")
	assert.Equal(t, corev1.DisruptionTarget, rules[0].OnPodConditions[0].Type)
	assert.Equal(t, batchv1.PodFailurePolicyActionFailJob, rules[1].Action, "a permanent render error is not retried")
	assert.Equal(t, []int32{renderjob.ExitPermanent}, rules[1].OnExitCodes.Values)
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "regcred"}}, job.Spec.Template.Spec.ImagePullSecrets)
	c := pod.Containers[0]
	assert.Equal(t, "ghcr.io/x/render:v1", c.Image)
	assert.True(t, *c.SecurityContext.ReadOnlyRootFilesystem)
	assert.True(t, *c.SecurityContext.RunAsNonRoot)
	assert.False(t, *c.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop)
	assert.Equal(t, "512Mi", c.Resources.Limits.Memory().String())
	var cfg renderjob.Config
	require.NoError(t, json.Unmarshal([]byte(c.Env[0].Value), &cfg))
	assert.Equal(t, "team", cfg.Namespace)
	assert.Equal(t, "env/prod", cfg.Git.RenderedBranch)
	var gitVol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "git" {
			gitVol = &pod.Volumes[i]
		}
	}
	require.NotNil(t, gitVol, "the Pipeline's own git Secret, token key only")
	assert.Equal(t, "git-creds", gitVol.Secret.SecretName)
	assert.Equal(t, []corev1.KeyToPath{{Key: "token", Path: "token"}}, gitVol.Secret.Items)

	msg := renderjob.Message(renderjob.Result{RenderRunResult: v1alpha1.RenderRunResult{CommitSHA: sha("c"), Branch: "env/prod",
		DryCommit: sha("d"), Renderer: "kustomize", Objects: 4, MarkerDigest: "m1"}})
	e.finishJob(t, job, true, corev1.ContainerStateTerminated{ExitCode: 0, Message: string(msg)})
	_, got = e.reconcile(t, "rr")
	assert.Equal(t, "Succeeded", got.Status.Phase)
	require.NotNil(t, got.Status.Result)
	assert.Equal(t, "m1", got.Status.Result.MarkerDigest)
	assert.Contains(t, got.Status.Message, "rendered 4 objects")

	// A finished RenderRun never runs again.
	require.NoError(t, e.c.Delete(context.Background(), job))
	_, got = e.reconcile(t, "rr")
	assert.Equal(t, "Succeeded", got.Status.Phase)
}

// TestRenderRun_Failures: a render error, running out of memory, a deleted
// or foreign Job, the deadline, no image and the controller's namespace each
// fail the RenderRun with why.
func TestRenderRun_Failures(t *testing.T) {
	start := func(t *testing.T, e *env) *batchv1.Job {
		e.reconcile(t, "rr")
		e.reconcile(t, "rr")
		return e.job(t, "rr")
	}
	t.Run("render error", func(t *testing.T) {
		e := newEnv(t, run("rr"))
		job := start(t, e)
		msg := renderjob.Message(renderjob.Result{Error: "render-manifests: rendered branch env/prod was changed outside kardinal"})
		e.finishJob(t, job, false, corev1.ContainerStateTerminated{ExitCode: 1, Message: string(msg)})
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Equal(t, "render-manifests: rendered branch env/prod was changed outside kardinal", got.Status.Message)
	})
	t.Run("out of memory", func(t *testing.T) {
		e := newEnv(t, run("rr"))
		job := start(t, e)
		e.finishJob(t, job, false, corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"})
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Equal(t, "the render ran out of memory (512Mi) and was stopped", got.Status.Message)
	})
	t.Run("Job deleted", func(t *testing.T) {
		e := newEnv(t, run("rr"))
		job := start(t, e)
		require.NoError(t, e.c.Delete(context.Background(), job))
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Contains(t, got.Status.Message, "was deleted before it finished")
	})
	t.Run("foreign Job", func(t *testing.T) {
		foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "rr", Namespace: "team"},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{Name: "x", Image: "evil"}}}}}}
		e := newEnv(t, run("rr"), foreign)
		e.reconcile(t, "rr")
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Contains(t, got.Status.Message, "is not owned by this RenderRun")
	})
	t.Run("deadline", func(t *testing.T) {
		e := newEnv(t, run("rr"))
		start(t, e)
		e.now = e.now.Add(6 * time.Minute)
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Running", got.Status.Phase, "the Job's own deadline (5m) ends it first; the reconciler waits 2m more")
		e.now = e.now.Add(2 * time.Minute)
		_, got = e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Contains(t, got.Status.Message, "did not finish within 5m0s")
		var jobs batchv1.JobList
		require.NoError(t, e.c.List(context.Background(), &jobs))
		assert.Empty(t, jobs.Items, "the Job is deleted")
	})
	t.Run("no image", func(t *testing.T) {
		e := newEnv(t, run("rr"))
		e.r.Image = ""
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Contains(t, got.Status.Message, "--render-image")
	})
	t.Run("controller namespace", func(t *testing.T) {
		e := newEnv(t, run("rr"))
		e.r.ControllerNamespace = "team"
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Contains(t, got.Status.Message, "controller's namespace")
	})
}

// TestRenderRun_KnownMarkerDigests: a render accepts on the rendered branch
// only a marker one of the newest earlier renders of the environment wrote.
func TestRenderRun_KnownMarkerDigests(t *testing.T) {
	done := func(name, digest string, at time.Time, branch string) *v1alpha1.RenderRun {
		r := run(name)
		r.Spec.Git.RenderedBranch = branch
		fin := metav1.NewTime(at)
		r.Status = v1alpha1.RenderRunStatus{Phase: "Succeeded", FinishedAt: &fin, Result: &v1alpha1.RenderRunResult{MarkerDigest: digest}}
		return r
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	other := run("other-env")
	other.Labels["kardinal.io/environment"] = "staging"
	e := newEnv(t, run("rr"), done("a", "d1", base, "env/prod"), done("b", "d2", base.Add(time.Hour), "env/prod"),
		done("c", "d3", base.Add(2*time.Hour), "env/other"), other)
	_, got := e.reconcile(t, "rr")
	assert.Equal(t, []string{"d2", "d1"}, got.Status.KnownMarkerDigests, "newest first, same branch only")
}

func sha(c string) string { return strings.Repeat(c, 40)[:40] }

// TestRenderRun_ResultTrust (QA round 2 on #1515, M2): the result is read
// only from a Pod the Job owns (by UID, with the Job's name prefix), and a
// result that cannot be true (another branch, a short commit id) fails the
// RenderRun instead of succeeding it.
func TestRenderRun_ResultTrust(t *testing.T) {
	good := v1alpha1.RenderRunResult{CommitSHA: sha("c"), Branch: "env/prod", DryCommit: sha("d"), Renderer: "kustomize", Objects: 1}
	start := func(t *testing.T) (*env, *batchv1.Job) {
		e := newEnv(t, run("rr"))
		e.reconcile(t, "rr")
		e.reconcile(t, "rr")
		return e, e.job(t, "rr")
	}
	t.Run("a Pod with the Job's labels it does not own", func(t *testing.T) {
		e, job := start(t)
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		require.NoError(t, e.c.Status().Update(context.Background(), job))
		forged := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "rr-forged", Namespace: "team",
			Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "render", Image: "i"}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "render", State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Message: string(renderjob.Message(renderjob.Result{RenderRunResult: good}))}}}}}}
		require.NoError(t, e.c.Create(context.Background(), forged))
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Failed", got.Status.Phase)
		assert.Contains(t, got.Status.Message, "its result is unknown", "the forged Pod is not read")
	})
	for name, tc := range map[string]struct {
		mutate func(*v1alpha1.RenderRunResult)
		want   string
	}{
		"another branch": {func(r *v1alpha1.RenderRunResult) { r.Branch = "main" }, `branch "main" is not "env/prod"`},
		"short commit":   {func(r *v1alpha1.RenderRunResult) { r.CommitSHA = "c0ffee" }, `commitSHA "c0ffee" is not a commit id`},
		"short dry":      {func(r *v1alpha1.RenderRunResult) { r.DryCommit = "d00d" }, `dryCommit "d00d" is not a commit id`},
		"unchanged with a branch": {func(r *v1alpha1.RenderRunResult) { r.NoChanges = true },
			"an unchanged render reports a pushed branch"},
		"unchanged without the head": {func(r *v1alpha1.RenderRunResult) { r.NoChanges, r.Branch, r.CommitSHA = true, "", "" },
			`commitSHA "" (the rendered branch's head) is not a commit id`},
	} {
		t.Run(name, func(t *testing.T) {
			e, job := start(t)
			res := good
			tc.mutate(&res)
			e.finishJob(t, job, true, corev1.ContainerStateTerminated{Message: string(renderjob.Message(renderjob.Result{RenderRunResult: res}))})
			_, got := e.reconcile(t, "rr")
			assert.Equal(t, "Failed", got.Status.Phase)
			assert.Contains(t, got.Status.Message, tc.want)
		})
	}
	t.Run("pr-review pushes the promotion branch", func(t *testing.T) {
		r := run("rr")
		r.Spec.Git.PullRequest = true
		e := newEnv(t, r)
		e.reconcile(t, "rr")
		e.reconcile(t, "rr")
		res := good
		res.Branch = stepsimpl.PRBranch(r.Namespace, "web-v2", "prod")
		e.finishJob(t, e.job(t, "rr"), true, corev1.ContainerStateTerminated{Message: string(renderjob.Message(renderjob.Result{RenderRunResult: res}))})
		_, got := e.reconcile(t, "rr")
		assert.Equal(t, "Succeeded", got.Status.Phase, got.Status.Message)
	})
}

// TestRenderRun_WaitingReason (QA round 2 on #1515, M3): a render Pod that
// cannot start says why in the RenderRun's message.
func TestRenderRun_WaitingReason(t *testing.T) {
	e := newEnv(t, run("rr"))
	e.reconcile(t, "rr")
	e.reconcile(t, "rr")
	job := e.job(t, "rr")
	yes := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "rr-abcde", Namespace: "team",
		Labels:          map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &yes}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "render", Image: "i"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "render", State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image ghcr.io/x/render:v1"}}}}}}
	require.NoError(t, e.c.Create(context.Background(), pod))
	_, got := e.reconcile(t, "rr")
	assert.Equal(t, "Running", got.Status.Phase)
	assert.Equal(t, "Job rr running; Pod rr-abcde: ImagePullBackOff: Back-off pulling image ghcr.io/x/render:v1", got.Status.Message)
}

// TestRenderRun_LostResult (QA round 2 on #1515, M1): a render whose result
// was lost (its Pod gone) fails, and the next RenderRun of the environment
// lists its Bundle as unconfirmed, so the render Job accepts the branch's
// marker if that render did push; a Succeeded render after it confirms the
// branch again.
func TestRenderRun_LostResult(t *testing.T) {
	ctx := context.Background()
	first := run("rr1")
	first.Spec.BundleName = "web-v2"
	e := newEnv(t, first)
	e.reconcile(t, "rr1")
	e.reconcile(t, "rr1")
	job := e.job(t, "rr1")
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	require.NoError(t, e.c.Status().Update(ctx, job))
	_, got := e.reconcile(t, "rr1")
	require.Equal(t, "Failed", got.Status.Phase)
	assert.Contains(t, got.Status.Message, "its result is unknown")

	second := run("rr2")
	second.Spec.BundleName = "web-v3"
	require.NoError(t, e.c.Create(ctx, second))
	e.now = e.now.Add(time.Minute)
	_, got = e.reconcile(t, "rr2")
	assert.Equal(t, []string{"web-v2"}, got.Status.UnconfirmedBundles)
	var cfg renderjob.Config
	e.reconcile(t, "rr2")
	require.NoError(t, json.Unmarshal([]byte(e.job(t, "rr2").Spec.Template.Spec.Containers[0].Env[0].Value), &cfg))
	assert.Equal(t, []string{"web-v2"}, cfg.UnconfirmedBundles, "the render Job gets them")

	e.finishJob(t, e.job(t, "rr2"), true, corev1.ContainerStateTerminated{Message: string(renderjob.Message(renderjob.Result{
		RenderRunResult: v1alpha1.RenderRunResult{CommitSHA: sha("c"), Branch: "env/prod", DryCommit: sha("d"), MarkerDigest: "m2"}}))})
	_, got = e.reconcile(t, "rr2")
	require.Equal(t, "Succeeded", got.Status.Phase)
	third := run("rr3")
	require.NoError(t, e.c.Create(ctx, third))
	e.now = e.now.Add(time.Minute)
	_, got = e.reconcile(t, "rr3")
	assert.Empty(t, got.Status.UnconfirmedBundles, "confirmed by the later Succeeded render")
	assert.Equal(t, []string{"m2"}, got.Status.KnownMarkerDigests)
}

// TestRenderRun_OnlyTheNewestLostResultIsUnconfirmed (QA round 3 on #1515):
// of two renders whose results were lost, only the newer one's Bundle is
// unconfirmed: the older one's push, if any, is not the branch's head, so
// neither a render nor a rollback adopts its marker.
func TestRenderRun_OnlyTheNewestLostResultIsUnconfirmed(t *testing.T) {
	ctx := context.Background()
	lose := func(e *env, name string) {
		t.Helper()
		e.reconcile(t, name)
		e.reconcile(t, name)
		job := e.job(t, name)
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		require.NoError(t, e.c.Status().Update(ctx, job))
		_, got := e.reconcile(t, name)
		require.Equal(t, "Failed", got.Status.Phase)
	}
	first := run("rr1")
	first.Spec.BundleName = "web-v2"
	e := newEnv(t, first)
	lose(e, "rr1")
	e.now = e.now.Add(time.Minute)
	second := run("rr2")
	second.Spec.BundleName = "web-v3"
	require.NoError(t, e.c.Create(ctx, second))
	lose(e, "rr2")

	e.now = e.now.Add(time.Minute)
	third := run("rr3")
	third.Spec.BundleName = "web-v4"
	require.NoError(t, e.c.Create(ctx, third))
	_, got := e.reconcile(t, "rr3")
	assert.Equal(t, []string{"web-v3"}, got.Status.UnconfirmedBundles)
}

// TestRenderRun_RetiredBundlesKeepTheirMarkers: a retired Bundle's Graph
// was deleted with its RenderRuns (#1492), so the marker digests of its
// renders come from its status.retiredSteps: a render after the retirement
// still knows them (newest first, with the RenderRuns that remain), and a
// retired step of another environment or Pipeline does not count.
func TestRenderRun_RetiredBundlesKeepTheirMarkers(t *testing.T) {
	at := func(m int) *metav1.Time {
		v := metav1.NewTime(time.Date(2026, 10, 9, 10, m, 0, 0, time.UTC))
		return &v
	}
	retired := func(name, pipeline string, steps ...v1alpha1.RetiredStep) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"},
			Spec: v1alpha1.BundleSpec{Pipeline: pipeline, Type: "image"}}
		b.Status.RetiredAt, b.Status.RetiredSteps = at(59), steps
		return b
	}
	d := func(c string) string { return strings.Repeat(c, 64) }
	old := retired("web-v1", "web", v1alpha1.RetiredStep{Name: "s1", Environment: "prod", State: "Verified", VerifiedAt: at(1), MarkerDigest: d("1")},
		v1alpha1.RetiredStep{Name: "s1t", Environment: "test", State: "Verified", VerifiedAt: at(1), MarkerDigest: d("9")})
	newer := retired("web-v2", "web", v1alpha1.RetiredStep{Name: "s2", Environment: "prod", State: "Verified", VerifiedAt: at(5), MarkerDigest: d("2")})
	other := retired("api-v1", "api", v1alpha1.RetiredStep{Name: "s3", Environment: "prod", State: "Verified", VerifiedAt: at(6), MarkerDigest: d("3")})
	notRetired := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "web-v3", Namespace: "team"}, Spec: v1alpha1.BundleSpec{Pipeline: "web", Type: "image"},
		Status: v1alpha1.BundleStatus{RetiredSteps: []v1alpha1.RetiredStep{{Name: "s4", Environment: "prod", MarkerDigest: d("4")}}}}

	rr := run("rr")
	rr.Spec.BundleName = "web-v9"
	e := newEnv(t, rr, old, newer, other, notRetired)
	_, got := e.reconcile(t, "rr")
	assert.Equal(t, []string{d("2"), d("1")}, got.Status.KnownMarkerDigests)
}

// TestRenderRun_RetiredLostRenderUnconfirmed: when the newest render of the
// environment is a retired Bundle's Failed step (its RenderRun deleted with
// the Graph), that Bundle is unconfirmed, as a Failed RenderRun would be; a
// later accepted render confirms the branch again.
func TestRenderRun_RetiredLostRenderUnconfirmed(t *testing.T) {
	at := func(m int) metav1.Time { return metav1.NewTime(time.Date(2026, 10, 9, 10, m, 0, 0, time.UTC)) }
	bundle := func(name string, rs v1alpha1.RetiredStep) *v1alpha1.Bundle {
		retiredAt := at(59)
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"}, Spec: v1alpha1.BundleSpec{Pipeline: "web", Type: "image"}}
		b.Status.RetiredAt, b.Status.RetiredSteps = &retiredAt, []v1alpha1.RetiredStep{rs}
		return b
	}
	verified := at(1)
	ok := bundle("web-v1", v1alpha1.RetiredStep{Name: "s1", Environment: "prod", State: "Verified", CreatedAt: at(0), VerifiedAt: &verified,
		MarkerDigest: strings.Repeat("1", 64)})
	lost := bundle("web-v2", v1alpha1.RetiredStep{Name: "s2", Environment: "prod", State: "Failed", CreatedAt: at(5), RenderRequested: true})
	older := bundle("web-v0", v1alpha1.RetiredStep{Name: "s0", Environment: "prod", State: "Failed", CreatedAt: at(-5), RenderRequested: true})
	// Failed before it asked for its render (newest, but rendered nothing).
	early := bundle("web-v2b", v1alpha1.RetiredStep{Name: "s2b", Environment: "prod", State: "Failed", CreatedAt: at(7)})
	rr := run("rr")
	rr.Spec.BundleName = "web-v3"
	e := newEnv(t, rr, ok, lost, older, early)
	_, got := e.reconcile(t, "rr")
	assert.Equal(t, []string{"web-v2"}, got.Status.UnconfirmedBundles)
	assert.Equal(t, []string{strings.Repeat("1", 64)}, got.Status.KnownMarkerDigests)
}
