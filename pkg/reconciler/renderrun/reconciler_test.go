// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderrun_test

import (
	"context"
	"encoding/json"
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
		AuthorName: "kardinal-promoter", AuthorEmail: "k@example.com", NowFn: func() time.Time { return e.now }}
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
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x", Namespace: "team",
		Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)}},
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
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
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

	msg := renderjob.Message(renderjob.Result{RenderRunResult: v1alpha1.RenderRunResult{CommitSHA: "c0ffee1234", Branch: "env/prod",
		DryCommit: "d00d", Renderer: "kustomize", Objects: 4, MarkerDigest: "m1"}})
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
