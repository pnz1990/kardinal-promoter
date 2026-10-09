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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
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
	old.Labels = genuine(map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "v1",
		"kardinal.io/environment": "prod", "kardinal.io/hook-phase": "pre"})
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

// TestHookRun_WaitsForSiblingNotYetDeleted: kro creates a renamed hook's new
// HookRun before it prunes the old one; the new run waits for the old Job
// even before the old HookRun is deleted (regression found live).
func TestHookRun_WaitsForSiblingNotYetDeleted(t *testing.T) {
	old := newHookRun(jobJSON(""))
	old.Name = "app-v1-prod-pre-old"
	old.Labels = genuine(map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "v1",
		"kardinal.io/environment": "prod", "kardinal.io/hook-phase": "pre"})
	deadline := metav1.NewTime(time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC))
	old.Status = v1alpha1.HookRunStatus{Phase: v1alpha1.HookRunRunning, JobUID: "u", Deadline: &deadline}
	cur := newHookRun(jobJSON(""))
	cur.Labels = old.Labels
	h := newHarness(t, old, cur)
	h.reconcile()
	h.reconcile()
	_, exists := h.job()
	assert.False(t, exists, "no Job while the sibling runs")

	// The sibling finished: the new run starts.
	var o v1alpha1.HookRun
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Name: old.Name, Namespace: ns}, &o))
	o.Status.Phase = v1alpha1.HookRunSucceeded
	require.NoError(t, h.c.Status().Update(context.Background(), &o))
	h.reconcile()
	_, exists = h.job()
	assert.True(t, exists)
}

// TestHookRun_WaitsForOlderPendingSibling: two HookRuns of one slot that
// both have no Job yet (a rename landing before the old run created its Job)
// do not both start: the newer waits for the older, the older does not wait
// for the newer, and a deleted older one without a Job does not block
// (regression, QA #1493 round 2).
func TestHookRun_WaitsForOlderPendingSibling(t *testing.T) {
	labels := genuine(map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "v1",
		"kardinal.io/environment": "prod", "kardinal.io/hook-phase": "pre"})
	t0 := metav1.NewTime(time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC))
	t1 := metav1.NewTime(t0.Add(time.Minute))
	deadline := metav1.NewTime(time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC))
	sibling := func(created metav1.Time, phase string) *v1alpha1.HookRun {
		o := newHookRun(jobJSON(""))
		o.Name, o.UID, o.Labels, o.CreationTimestamp = "app-v1-prod-pre-other", "other-uid", labels, created
		o.Status = v1alpha1.HookRunStatus{Phase: phase, SpecHash: "x", Deadline: &deadline}
		return o
	}
	cases := []struct {
		name    string
		sibling *v1alpha1.HookRun
		curAt   metav1.Time
		waits   bool
	}{
		{"older Pending sibling", sibling(t0, v1alpha1.HookRunPending), t1, true},
		{"older sibling not started", sibling(t0, ""), t1, true},
		{"newer Pending sibling", sibling(t1, v1alpha1.HookRunPending), t0, false},
		{"older sibling Succeeded", sibling(t0, v1alpha1.HookRunSucceeded), t1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur := newHookRun(jobJSON(""))
			cur.Labels, cur.CreationTimestamp = labels, tc.curAt
			h := newHarness(t, tc.sibling, cur)
			h.reconcile()
			h.reconcile()
			_, exists := h.job()
			assert.Equal(t, !tc.waits, exists)
			if tc.waits {
				assert.Contains(t, h.hookRun().Status.Message, "waiting for HookRun app-v1-prod-pre-other")
			}
		})
	}

	// A deleted older sibling without a Job never starts: it does not block.
	old := sibling(t0, v1alpha1.HookRunPending)
	now := metav1.NewTime(t1.Time)
	old.DeletionTimestamp, old.Finalizers = &now, []string{hookrun.Finalizer}
	cur := newHookRun(jobJSON(""))
	cur.Labels, cur.CreationTimestamp = labels, t1
	h := newHarness(t, old, cur)
	h.reconcile()
	h.reconcile()
	_, exists := h.job()
	assert.True(t, exists)
}

// TestHookRun_IgnoresForgedHookRun: a HookRun kro did not apply (no
// kro.run/node-id), or whose kardinal.io/bundle-uid is not its Bundle's UID,
// or whose Bundle does not exist, gets no Job and no status; a forged Running
// sibling does not hold a genuine run (regression, QA #1493 round 2).
func TestHookRun_IgnoresForgedHookRun(t *testing.T) {
	cases := map[string]func(hr *v1alpha1.HookRun){
		"no kro label":     func(hr *v1alpha1.HookRun) { delete(hr.Labels, graph.LabelKRONodeID) },
		"no bundle uid":    func(hr *v1alpha1.HookRun) { delete(hr.Labels, graph.LabelBundleUID) },
		"wrong bundle uid": func(hr *v1alpha1.HookRun) { hr.Labels[graph.LabelBundleUID] = "old-bundle-uid" },
		"no such bundle":   func(hr *v1alpha1.HookRun) { hr.Spec.BundleName = "gone" },
	}
	for name, forge := range cases {
		t.Run(name, func(t *testing.T) {
			hr := newHookRun(jobJSON(""))
			forge(hr)
			h := newHarness(t, hr)
			h.reconcile()
			h.reconcile()
			_, exists := h.job()
			assert.False(t, exists, "no Job for a forged HookRun")
			got := h.hookRun()
			assert.Empty(t, got.Status.Phase)
			assert.Empty(t, got.Finalizers)
		})
	}

	forged := newHookRun(jobJSON(""))
	forged.Name, forged.UID = "app-v1-prod-pre-forged", "forged-uid"
	forged.Labels = map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "v1",
		"kardinal.io/environment": "prod", "kardinal.io/hook-phase": "pre", graph.LabelBundleUID: testBundleUID}
	deadline := metav1.NewTime(time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC))
	forged.Status = v1alpha1.HookRunStatus{Phase: v1alpha1.HookRunRunning, JobUID: "u", Deadline: &deadline}
	cur := newHookRun(jobJSON(""))
	cur.Labels = genuine(forged.Labels)
	h := newHarness(t, forged, cur)
	h.reconcile()
	h.reconcile()
	_, exists := h.job()
	assert.True(t, exists, "a forged Running sibling does not hold the genuine run")
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

// TestHookRun_SecurityDefaults: hook Pods are checked with the upstream Pod
// Security Admission evaluator at --hook-pod-security-level (baseline by
// default); below privileged, nodeName and hostPort are refused too; a Job
// selector and a hook in the controller's namespace are refused at every
// level (QA #1493: the hand-written list missed checks PSA has).
func TestHookRun_SecurityDefaults(t *testing.T) {
	c := `"containers":[{"name":"m","image":"busybox"%s}]`
	restrictedSC := `,"securityContext":{"runAsNonRoot":true,"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"seccompProfile":{"type":"RuntimeDefault"}}`
	cases := []struct {
		name, job, want string
		// refused at baseline, restricted and privileged
		baseline, restricted, privileged bool
	}{
		{"plain pod", podJob(`{` + sprintf(c, "") + `}`), "restricted", false, true, false},
		{"restricted pod", podJob(`{` + sprintf(c, restrictedSC) + `}`), "", false, false, false},
		{"privileged", podJob(`{` + sprintf(c, `,"securityContext":{"privileged":true}`) + `}`), "privileged", true, true, false},
		{"escalation", podJob(`{` + sprintf(c, `,"securityContext":{"allowPrivilegeEscalation":true}`) + `}`), "allowPrivilegeEscalation", false, true, false},
		{"capabilities", podJob(`{` + sprintf(c, `,"securityContext":{"capabilities":{"add":["NET_ADMIN"]}}`) + `}`), "capabilities", true, true, false},
		{"hostNetwork", podJob(`{"hostNetwork":true,` + sprintf(c, "") + `}`), "host namespaces", true, true, false},
		{"hostPID", podJob(`{"hostPID":true,` + sprintf(c, "") + `}`), "host namespaces", true, true, false},
		{"hostPath", podJob(`{"volumes":[{"name":"root","hostPath":{"path":"/"}}],` + sprintf(c, "") + `}`), "hostPath", true, true, false},
		{"procMount", podJob(`{` + sprintf(c, `,"securityContext":{"procMount":"Unmasked"}`) + `}`), "procMount", true, true, false},
		{"unsafe sysctl", podJob(`{"securityContext":{"sysctls":[{"name":"kernel.msgmax","value":"1"}]},` + sprintf(c, "") + `}`), "sysctls", true, true, false},
		{"seccomp unconfined", podJob(`{"securityContext":{"seccompProfile":{"type":"Unconfined"}},` + sprintf(c, "") + `}`), "seccompProfile", true, true, false},
		{"nodeName", podJob(`{"nodeName":"n1",` + sprintf(c, restrictedSC) + `}`), "nodeName", true, true, false},
		{"hostPort", podJob(`{` + sprintf(c, `,"ports":[{"containerPort":80,"hostPort":80}]`) + `}`), "hostPort", true, true, false},
		// AppArmor through the pod template's annotation (QA #1493 round 3:
		// the template metadata was not passed to the evaluator).
		{"apparmor annotation", `{"backoffLimit":0,"template":{"metadata":{"annotations":{"container.apparmor.security.beta.kubernetes.io/m":"unconfined"}},"spec":{` +
			sprintf(c, restrictedSC) + `}}}`, "AppArmor", true, true, false},
		{"ephemeral volume", podJob(`{"volumes":[{"name":"scratch","ephemeral":{"volumeClaimTemplate":{"spec":{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"1Gi"}}}}}}],` + sprintf(c, restrictedSC) + `}`), "", false, false, false},
		{"selector", `{"manualSelector":true,"selector":{"matchLabels":{"a":"b"}},"template":{"spec":{` + sprintf(c, "") + `}}}`, "selector", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, lv := range []struct {
				level   string
				refused bool
			}{{"", tc.baseline}, {"restricted", tc.restricted}, {"privileged", tc.privileged}} {
				h := newHarness(t, newHookRun(tc.job))
				h.r.PodSecurityLevel = lv.level
				h.reconcile()
				hr := h.hookRun()
				if !lv.refused {
					assert.Equal(t, v1alpha1.HookRunPending, hr.Status.Phase, "level %q: %s", lv.level, hr.Status.Message)
					continue
				}
				assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase, "level %q", lv.level)
				assert.Contains(t, hr.Status.Message, tc.want, "level %q", lv.level)
				assert.NotContains(t, hr.Status.Message, "uses a hostPort in container", "hostPort reported once, by the standard")
				_, exists := h.job()
				assert.False(t, exists, "level %q: no Job", lv.level)
			}
		})
	}

	inController := newHookRun(jobJSON(""))
	h := newHarness(t, inController)
	h.r.ControllerNamespace = ns
	h.r.PodSecurityLevel = "privileged"
	h.reconcile()
	assert.Equal(t, v1alpha1.HookRunFailed, h.hookRun().Status.Phase)
	assert.Contains(t, h.hookRun().Status.Message, "controller's namespace")
}

// TestParsePodSecurityLevel: the flag accepts the three PSA levels.
func TestParsePodSecurityLevel(t *testing.T) {
	for in, want := range map[string]string{"": "baseline", "baseline": "baseline", "restricted": "restricted", "privileged": "privileged"} {
		got, err := hookrun.ParsePodSecurityLevel(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := hookrun.ParsePodSecurityLevel("strict")
	require.Error(t, err)
}

func sprintf(format, arg string) string { return fmt.Sprintf(format, arg) }
