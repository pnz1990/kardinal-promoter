// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hookrun_test

// Regression tests for the QA review of #1493.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/hookrun"
)

func podJob(pod string) string {
	return `{"template":{"spec":` + pod + `}}`
}

// TestHookRun_TTLStrippedAndBackoffZero: ttlSecondsAfterFinished is dropped
// (a Job deleted as it finishes would read as deleted before it finished)
// and backoffLimit defaults to 0 (a migration is not retried by default).
func TestHookRun_TTLStrippedAndBackoffZero(t *testing.T) {
	h := newHarness(t, newHookRun(`{"ttlSecondsAfterFinished":0,"template":{"spec":{"containers":[{"name":"m","image":"busybox"}]}}}`))
	h.run()
	j, ok := h.job()
	require.True(t, ok)
	assert.Nil(t, j.Spec.TTLSecondsAfterFinished)
	require.NotNil(t, j.Spec.BackoffLimit)
	assert.Equal(t, int32(0), *j.Spec.BackoffLimit)
}

// TestHookRun_StaleCacheCannotOverwriteTerminal: a reconcile working from a
// stale copy (Running) after the HookRun is Succeeded, with the Job gone,
// would have written Failed; the optimistic lock refuses the write.
func TestHookRun_StaleCacheCannotOverwriteTerminal(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	stale := h.hookRun() // Running
	h.setJobCondition(batchv1.JobComplete, "", "")
	h.reconcile()
	require.Equal(t, v1alpha1.HookRunSucceeded, h.hookRun().Status.Phase)
	j, _ := h.job()
	require.NoError(t, h.c.Delete(context.Background(), j))

	staleClient := interceptor.NewClient(h.c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if hr, ok := obj.(*v1alpha1.HookRun); ok {
				stale.DeepCopyInto(hr)
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r := *h.r
	r.Client = staleClient
	r.APIReader = h.c
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: stale.Name, Namespace: ns}})
	require.Error(t, err, "the stale write is refused")
	assert.Equal(t, v1alpha1.HookRunSucceeded, h.hookRun().Status.Phase, "Succeeded is kept")
}

// TestHookRun_CacheLagIsNotDeletion: a Job the cache does not have yet (it
// was just created) is found through the API reader, not taken as deleted.
func TestHookRun_CacheLagIsNotDeletion(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	lagging := interceptor.NewClient(h.c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*batchv1.Job); ok {
				return c.Get(ctx, types.NamespacedName{Namespace: key.Namespace, Name: "missing"}, obj, opts...)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r := *h.r
	r.Client = lagging
	r.APIReader = h.c
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1-prod-pre-migrate", Namespace: ns}})
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.HookRunRunning, h.hookRun().Status.Phase)
}

// TestHookRun_ForeignUnlabelledJob: a Job of the HookRun's name outside the
// cache (no kardinal.io/hookrun label) fails the HookRun with a clear
// message instead of looping on AlreadyExists.
func TestHookRun_ForeignUnlabelledJob(t *testing.T) {
	foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod-pre-migrate", Namespace: ns},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "x"}}}}}}
	api := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(foreign).Build()
	h := newHarness(t, newHookRun(jobJSON("")))
	cache := interceptor.NewClient(h.c.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*batchv1.Job); ok {
				return api.Create(ctx, obj.DeepCopyObject().(client.Object))
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	h.r.Client, h.r.APIReader = cache, api
	h.reconcile()
	h.reconcile()
	hr := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
	assert.Contains(t, hr.Status.Message, "not owned by this HookRun")
}

// TestHookRun_FinalizerHoldsRunningJob: a HookRun deleted while its Job runs
// (the hook renamed or removed mid-flight, or the Bundle deleted) stays
// until the Job ends, so the migration is not cut off.
func TestHookRun_FinalizerHoldsRunningJob(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	hr := h.hookRun()
	require.True(t, controllerutil.ContainsFinalizer(hr, hookrun.Finalizer))
	require.NoError(t, h.c.Delete(context.Background(), hr))
	res := h.reconcile()
	assert.Positive(t, res.RequeueAfter)
	hr = h.hookRun()
	assert.True(t, controllerutil.ContainsFinalizer(hr, hookrun.Finalizer), "held while the Job runs")

	h.setJobCondition(batchv1.JobComplete, "", "")
	h.reconcile()
	var gone v1alpha1.HookRun
	err := h.c.Get(context.Background(), types.NamespacedName{Name: hr.Name, Namespace: ns}, &gone)
	assert.True(t, err != nil, "released once the Job ended")
}

// TestHookRun_FinalizerReleasedAtDeadline: a Job past its deadline does not
// hold the deletion.
func TestHookRun_FinalizerReleasedAtDeadline(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	require.NoError(t, h.c.Delete(context.Background(), h.hookRun()))
	h.now = h.now.Add(11 * time.Minute)
	h.reconcile()
	var gone v1alpha1.HookRun
	err := h.c.Get(context.Background(), types.NamespacedName{Name: "app-v1-prod-pre-migrate", Namespace: ns}, &gone)
	assert.Error(t, err)
}

// TestHookRun_WaitsForRemovedSibling: a renamed hook's new HookRun does not
// start while the old one (being deleted) still runs: no overlapping
// migration.
func TestHookRun_WaitsForRemovedSibling(t *testing.T) {
	old := newHookRun(jobJSON(""))
	old.Name = "app-v1-prod-pre-old"
	old.Labels = map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "v1",
		"kardinal.io/environment": "prod", "kardinal.io/hook-phase": "pre"}
	old.Finalizers = []string{hookrun.Finalizer}
	deadline := metav1.NewTime(time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC))
	old.Status = v1alpha1.HookRunStatus{Phase: v1alpha1.HookRunRunning, JobUID: "u", Deadline: &deadline}
	cur := newHookRun(jobJSON(""))
	cur.Labels = old.Labels
	h := newHarness(t, old, cur)
	require.NoError(t, h.c.Delete(context.Background(), old)) // finalizer: stays, being deleted
	h.reconcile()                                             // start
	h.reconcile()
	_, exists := h.job()
	assert.False(t, exists, "no Job while the removed sibling runs")
	assert.Contains(t, h.hookRun().Status.Message, "waiting for HookRun app-v1-prod-pre-old")
}

// TestHookRun_SkippedWhenStepAdvanced: a hook added after its step started
// is Skipped, with no Job.
func TestHookRun_SkippedWhenStepAdvanced(t *testing.T) {
	hr := newHookRun(jobJSON(""))
	hr.Spec.StepAdvanced = true
	h := newHarness(t, hr)
	h.reconcile()
	got := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunSkipped, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "added to the Pipeline after the step had already started")
	h.reconcile()
	_, exists := h.job()
	assert.False(t, exists)
}

// TestHookRun_SecurityDefaults: privileged Pods, host namespaces and ports,
// hostPath, nodeName, added capabilities and a Job selector are refused
// unless --hook-allow-privileged; a hook in the controller's namespace is
// refused always.
func TestHookRun_SecurityDefaults(t *testing.T) {
	c := `"containers":[{"name":"m","image":"busybox"%s}]`
	cases := []struct {
		name, job, want string
		allowed         bool // allowed with --hook-allow-privileged
	}{
		{"privileged", podJob(`{` + sprintf(c, `,"securityContext":{"privileged":true}`) + `}`), "privileged", true},
		{"escalation", podJob(`{` + sprintf(c, `,"securityContext":{"allowPrivilegeEscalation":true}`) + `}`), "privilege escalation", true},
		{"capabilities", podJob(`{` + sprintf(c, `,"securityContext":{"capabilities":{"add":["NET_ADMIN"]}}`) + `}`), "adds capabilities", true},
		{"hostNetwork", podJob(`{"hostNetwork":true,` + sprintf(c, "") + `}`), "hostNetwork", true},
		{"hostPID", podJob(`{"hostPID":true,` + sprintf(c, "") + `}`), "hostPID", true},
		{"hostPath", podJob(`{"volumes":[{"name":"root","hostPath":{"path":"/"}}],` + sprintf(c, "") + `}`), "hostPath", true},
		{"nodeName", podJob(`{"nodeName":"n1",` + sprintf(c, "") + `}`), "nodeName", true},
		{"hostPort", podJob(`{` + sprintf(c, `,"ports":[{"containerPort":80,"hostPort":80}]`) + `}`), "hostPort", true},
		{"selector", `{"manualSelector":true,"selector":{"matchLabels":{"a":"b"}},"template":{"spec":{` + sprintf(c, "") + `}}}`, "selector", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, newHookRun(tc.job))
			h.reconcile()
			hr := h.hookRun()
			assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
			assert.Contains(t, hr.Status.Message, tc.want)

			h2 := newHarness(t, newHookRun(tc.job))
			h2.r.AllowPrivileged = true
			h2.reconcile()
			if tc.allowed {
				assert.Equal(t, v1alpha1.HookRunPending, h2.hookRun().Status.Phase, "allowed with --hook-allow-privileged")
			} else {
				assert.Equal(t, v1alpha1.HookRunFailed, h2.hookRun().Status.Phase)
			}
		})
	}

	inController := newHookRun(jobJSON(""))
	h := newHarness(t, inController)
	h.r.ControllerNamespace = ns
	h.reconcile()
	assert.Equal(t, v1alpha1.HookRunFailed, h.hookRun().Status.Phase)
	assert.Contains(t, h.hookRun().Status.Message, "controller's namespace")
}

func sprintf(format, arg string) string { return fmt.Sprintf(format, arg) }
