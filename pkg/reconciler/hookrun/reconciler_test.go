// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hookrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/hookrun"
)

const ns = "team-a"

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func jobJSON(sa string) string {
	saField := ""
	if sa != "" {
		saField = `"serviceAccountName":"` + sa + `",`
	}
	return `{"backoffLimit":0,"template":{"spec":{` + saField +
		`"containers":[{"name":"m","image":"busybox:1.36","command":["sh","-c","echo ${X}"]}]}}}`
}

func newHookRun(job string) *v1alpha1.HookRun {
	return &v1alpha1.HookRun{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod-pre-migrate", Namespace: ns, UID: types.UID("hr-uid")},
		Spec: v1alpha1.HookRunSpec{
			PipelineName: "app", BundleName: "v1", Environment: "prod", Hook: "migrate", Phase: "pre",
			Job: runtime.RawExtension{Raw: []byte(job)}, Timeout: "10m",
		},
	}
}

type harness struct {
	t   *testing.T
	c   client.Client
	r   *hookrun.Reconciler
	now time.Time
	// creates counts Job creates that reached the API.
	creates int
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	h := &harness{t: t, now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	h.c = fake.NewClientBuilder().WithScheme(scheme(t)).
		WithStatusSubresource(&v1alpha1.HookRun{}, &batchv1.Job{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*batchv1.Job); ok {
				obj.SetUID(uuid.NewUUID())
				if err := c.Create(ctx, obj, opts...); err != nil {
					return err
				}
				h.creates++
				return nil
			}
			return c.Create(ctx, obj, opts...)
		}}).Build()
	h.r = &hookrun.Reconciler{Client: h.c, AllowedServiceAccounts: []string{"default", "migrator"},
		GraphServiceAccount: "kardinal-graph", NowFn: func() time.Time { return h.now }}
	return h
}

func (h *harness) reconcile() ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1-prod-pre-migrate", Namespace: ns}})
	require.NoError(h.t, err)
	return res
}

func (h *harness) hookRun() *v1alpha1.HookRun {
	h.t.Helper()
	var hr v1alpha1.HookRun
	require.NoError(h.t, h.c.Get(context.Background(), types.NamespacedName{Name: "app-v1-prod-pre-migrate", Namespace: ns}, &hr))
	return &hr
}

func (h *harness) job() (*batchv1.Job, bool) {
	h.t.Helper()
	var j batchv1.Job
	err := h.c.Get(context.Background(), types.NamespacedName{Name: "app-v1-prod-pre-migrate", Namespace: ns}, &j)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(h.t, err)
	return &j, true
}

func (h *harness) setJobCondition(t batchv1.JobConditionType, reason, msg string) {
	h.t.Helper()
	j, ok := h.job()
	require.True(h.t, ok)
	j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{Type: t, Status: corev1.ConditionTrue, Reason: reason, Message: msg})
	require.NoError(h.t, h.c.Status().Update(context.Background(), j))
}

// run reconciles until the HookRun runs its Job.
func (h *harness) run() {
	h.t.Helper()
	h.reconcile() // start
	h.reconcile() // create Job
	require.Equal(h.t, v1alpha1.HookRunRunning, h.hookRun().Status.Phase)
}

// TestHookRun_CreatesOwnedJob: the first reconcile records the start and the
// deadline, the second creates the Job: owned by the HookRun (garbage
// collection deletes it and its Pods with the HookRun), restartPolicy Never,
// activeDeadlineSeconds the timeout, labelled for the cache.
func TestHookRun_CreatesOwnedJob(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.reconcile()
	hr := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunPending, hr.Status.Phase)
	assert.NotEmpty(t, hr.Status.SpecHash)
	require.NotNil(t, hr.Status.Deadline)
	assert.Equal(t, h.now.Add(10*time.Minute), hr.Status.Deadline.UTC())
	_, exists := h.job()
	assert.False(t, exists, "the Job is created after the start is recorded")

	h.reconcile()
	j, ok := h.job()
	require.True(t, ok, "job not created: %+v", h.hookRun().Status)
	owner := metav1.GetControllerOf(j)
	require.NotNil(t, owner)
	assert.Equal(t, "HookRun", owner.Kind)
	assert.Equal(t, types.UID("hr-uid"), owner.UID)
	assert.Equal(t, corev1.RestartPolicyNever, j.Spec.Template.Spec.RestartPolicy)
	require.NotNil(t, j.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int64(600), *j.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, "app-v1-prod-pre-migrate", j.Labels[hookrun.LabelHookRun])
	assert.Equal(t, "app-v1-prod-pre-migrate", j.Spec.Template.Labels[hookrun.LabelHookRun])
	assert.Equal(t, []string{"sh", "-c", "echo ${X}"}, j.Spec.Template.Spec.Containers[0].Command)
	hr = h.hookRun()
	assert.Equal(t, v1alpha1.HookRunRunning, hr.Status.Phase)
	assert.Equal(t, string(j.UID), hr.Status.JobUID)
}

// TestHookRun_Outcomes: the Job's Complete or Failed condition becomes the
// HookRun's terminal phase, and a finished HookRun never changes or creates
// a Job again, whatever happens to the Job.
func TestHookRun_Outcomes(t *testing.T) {
	cases := []struct {
		name      string
		cond      batchv1.JobConditionType
		reason    string
		wantPhase string
		wantMsg   string
	}{
		{"complete", batchv1.JobComplete, "", v1alpha1.HookRunSucceeded, "completed"},
		{"failed", batchv1.JobFailed, "BackoffLimitExceeded", v1alpha1.HookRunFailed, "BackoffLimitExceeded"},
		{"deadline", batchv1.JobFailed, "DeadlineExceeded", v1alpha1.HookRunFailed, "DeadlineExceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, newHookRun(jobJSON("")))
			h.run()
			h.setJobCondition(tc.cond, tc.reason, "")
			h.reconcile()
			hr := h.hookRun()
			assert.Equal(t, tc.wantPhase, hr.Status.Phase)
			assert.Contains(t, hr.Status.Message, tc.wantMsg)
			require.NotNil(t, hr.Status.FinishedAt)

			// The Job is deleted (TTL controller, kubectl, a cleanup tool):
			// the HookRun keeps its result and does not run the hook again.
			j, _ := h.job()
			require.NoError(t, h.c.Delete(context.Background(), j))
			for i := 0; i < 3; i++ {
				h.reconcile()
			}
			_, exists := h.job()
			assert.False(t, exists, "a finished hook is not run again")
			assert.Equal(t, 1, h.creates)
			assert.Equal(t, tc.wantPhase, h.hookRun().Status.Phase)
		})
	}
}

// TestHookRun_IdempotentAfterCrash: a crash between the Job create and the
// status write leaves a Job and a HookRun without status.jobUID. The next
// reconcile adopts the Job (it is the HookRun's) instead of failing or
// creating another.
func TestHookRun_IdempotentAfterCrash(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.reconcile() // start recorded
	hr := h.hookRun()
	ctrlTrue := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: hr.Name, Namespace: ns,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "kardinal.io/v1alpha1", Kind: "HookRun",
			Name: hr.Name, UID: hr.UID, Controller: &ctrlTrue}}},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{Name: "m", Image: "busybox"}}}}}}
	require.NoError(t, h.c.Create(context.Background(), job))
	created := h.creates

	h.reconcile()
	hr = h.hookRun()
	assert.Equal(t, v1alpha1.HookRunRunning, hr.Status.Phase)
	j, _ := h.job()
	assert.Equal(t, string(j.UID), hr.Status.JobUID, "the Job is adopted")
	assert.Equal(t, created, h.creates, "no second Job")

	// Re-running a reconcile that already did its work changes nothing.
	before := hr.Status
	h.reconcile()
	h.reconcile()
	assert.Equal(t, before, h.hookRun().Status)
}

// TestHookRun_ForeignJob: a Job of the HookRun's name that the HookRun does
// not own is not taken as the hook's run.
func TestHookRun_ForeignJob(t *testing.T) {
	foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod-pre-migrate", Namespace: ns},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "m", Image: "busybox"}}}}}}
	h := newHarness(t, newHookRun(jobJSON("")), foreign)
	h.reconcile()
	h.reconcile()
	hr := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
	assert.Contains(t, hr.Status.Message, "not owned by this HookRun")
}

// TestHookRun_JobDeletedWhileRunning: a Job deleted before it finished fails
// the HookRun; the hook (a migration, say) is not started a second time.
func TestHookRun_JobDeletedWhileRunning(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	j, _ := h.job()
	require.NoError(t, h.c.Delete(context.Background(), j))
	h.reconcile()
	hr := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
	assert.Contains(t, hr.Status.Message, "deleted before it finished")
	h.reconcile()
	_, exists := h.job()
	assert.False(t, exists)
	assert.Equal(t, 1, h.creates)
}

// TestHookRun_Timeout: past the deadline a running Job is deleted and the
// HookRun fails.
func TestHookRun_Timeout(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	res := h.reconcile()
	assert.Positive(t, res.RequeueAfter, "a running hook requeues for its deadline")
	h.now = h.now.Add(11 * time.Minute)
	h.reconcile()
	hr := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
	assert.Contains(t, hr.Status.Message, "timeout")
	_, exists := h.job()
	assert.False(t, exists, "the timed-out Job is deleted")
}

// TestHookRun_ServiceAccounts: the Pod may run only as an allowlisted
// ServiceAccount, never as the Graph's; a rejected hook fails without a Job.
func TestHookRun_ServiceAccounts(t *testing.T) {
	cases := []struct {
		sa      string
		allowed bool
		msg     string
	}{
		{"", true, ""},
		{"default", true, ""},
		{"migrator", true, ""},
		{"admin", false, "not in the controller's --hook-service-accounts"},
		{"kardinal-graph", false, "Graph ServiceAccount"},
	}
	for _, tc := range cases {
		t.Run("sa="+tc.sa, func(t *testing.T) {
			h := newHarness(t, newHookRun(jobJSON(tc.sa)))
			h.reconcile()
			h.reconcile()
			hr := h.hookRun()
			_, exists := h.job()
			if tc.allowed {
				assert.Equal(t, v1alpha1.HookRunRunning, hr.Status.Phase)
				assert.True(t, exists)
				return
			}
			assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
			assert.Contains(t, hr.Status.Message, tc.msg)
			assert.False(t, exists)
		})
	}
}

// TestHookRun_EmptyAllowlistMeansDefault: with no allowlist only the
// namespace's default ServiceAccount is allowed.
func TestHookRun_EmptyAllowlistMeansDefault(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("migrator")))
	h.r.AllowedServiceAccounts = nil
	h.reconcile()
	assert.Equal(t, v1alpha1.HookRunFailed, h.hookRun().Status.Phase)
}

// TestHookRun_InvalidJob: a job that is not a JobSpec fails at start.
func TestHookRun_InvalidJob(t *testing.T) {
	h := newHarness(t, newHookRun(`{"template":{"spec":{"containerz":[]}}}`))
	h.reconcile()
	hr := h.hookRun()
	assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
	assert.Contains(t, hr.Status.Message, "not a batch/v1 JobSpec")
}

// TestHookRun_SpecChangedAfterStart: a Pipeline edit while the hook runs
// updates the HookRun spec (kro re-applies it); the running Job is left as
// it is and the HookRun says the change was not applied.
func TestHookRun_SpecChangedAfterStart(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.run()
	hr := h.hookRun()
	hr.Spec.Job = runtime.RawExtension{Raw: []byte(jobJSON("migrator"))}
	require.NoError(t, h.c.Update(context.Background(), hr))
	h.reconcile()
	hr = h.hookRun()
	assert.True(t, meta.IsStatusConditionTrue(hr.Status.Conditions, hookrun.ConditionSpecChangedAfterStart))
	assert.Equal(t, v1alpha1.HookRunRunning, hr.Status.Phase)
	j, _ := h.job()
	assert.Empty(t, j.Spec.Template.Spec.ServiceAccountName, "the running Job is not changed")
	assert.Equal(t, 1, h.creates)
}

// TestHookRun_SpecChangedBeforeJob: the spec changed between the recorded
// start and the Job create (a crash in between): the HookRun fails rather
// than run a hook nobody started.
func TestHookRun_SpecChangedBeforeJob(t *testing.T) {
	h := newHarness(t, newHookRun(jobJSON("")))
	h.reconcile()
	hr := h.hookRun()
	hr.Spec.Timeout = "20m"
	require.NoError(t, h.c.Update(context.Background(), hr))
	h.reconcile()
	hr = h.hookRun()
	assert.Equal(t, v1alpha1.HookRunFailed, hr.Status.Phase)
	_, exists := h.job()
	assert.False(t, exists)
}

// TestHookRun_DeletedIsNoop: a HookRun being deleted (its Graph went away)
// is left to garbage collection.
func TestHookRun_DeletedIsNoop(t *testing.T) {
	hr := newHookRun(jobJSON(""))
	hr.Finalizers = []string{"test/keep"}
	h := newHarness(t, hr)
	require.NoError(t, h.c.Delete(context.Background(), hr))
	h.reconcile()
	_, exists := h.job()
	assert.False(t, exists)
	h2 := newHarness(t)
	h2.reconcile() // a HookRun that is gone: no error
}
