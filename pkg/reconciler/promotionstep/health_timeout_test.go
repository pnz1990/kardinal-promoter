// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

const badImage = "ghcr.io/org/app:v2-bad"

// crashLoopDeployment returns the status a 1-replica Deployment keeps while
// the pod of its new image crash-loops. The new ReplicaSet exists
// (Progressing=True, reason ReplicaSetUpdated) but its pod never becomes
// available, and Kubernetes reports nothing else until progressDeadlineSeconds.
// With RollingUpdate the old pod keeps serving (2 replicas, 1 updated,
// Available=True); with Recreate the old pod is gone (1 replica, 0 available,
// Available=False).
func crashLoopDeployment(name, ns string, strategy appsv1.DeploymentStrategyType) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &one, Strategy: appsv1.DeploymentStrategy{Type: strategy}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, UnavailableReplicas: 1},
	}
	progressing := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue,
		Reason: "ReplicaSetUpdated", Message: `ReplicaSet "` + name + `-5d4f8" is progressing.`}
	if strategy == appsv1.RecreateDeploymentStrategyType {
		d.Status.Replicas = 1
		d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable,
			Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable",
			Message: "Deployment does not have minimum availability."}, progressing}
	} else {
		d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = 2, 1, 1
		d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable,
			Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable",
			Message: "Deployment has minimum availability."}, progressing}
	}
	return withImage(d, badImage)
}

// verifiedBefore returns Bundle b0, which carries the image before the bad
// one, and its Verified step in env: the history lifecycle.PlanRollback picks
// the rollback target from.
func verifiedBefore(env string) []client.Object {
	b0 := makeBundle("b0", "p")
	b0.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}
	b0.Status.Phase = "Superseded"
	s0 := labelled(makeStep("step-b0", "p", "b0", env))
	s0.Status = v1alpha1.PromotionStepStatus{State: "Verified", Conditions: []metav1.Condition{{
		Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour))}}}
	return []client.Object{b0, s0}
}

func getBundle(c client.Client, name string) (*v1alpha1.Bundle, error) {
	var b v1alpha1.Bundle
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// TestCrashLoopRollsBackAtHealthTimeout proves that a crash-looping new image
// is rolled back within health.timeout. Kubernetes reports such a Deployment
// as still rolling out, which is not a health failure, so no check fails
// before the timeout; reaching health.timeout without a Healthy result counts
// as a health failure and applies onHealthFailure. The rollback Bundle carries
// the images of the Bundle verified before the failing one.
func TestCrashLoopRollsBackAtHealthTimeout(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
		OnHealthFailure: "rollback"}
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2-bad"}}
	for _, strategy := range []appsv1.DeploymentStrategyType{appsv1.RollingUpdateDeploymentStrategyType, appsv1.RecreateDeploymentStrategyType} {
		t.Run(string(strategy), func(t *testing.T) {
			deploy := crashLoopDeployment("p", "test", strategy)

			// Within the timeout: rolling out, not a failure, no rollback.
			objs := func() []client.Object { return append(verifiedBefore("test"), deploy.DeepCopy()) }
			c, got, _ := healthCase{env: env, images: images, objs: objs()}.run(t)
			require.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
			assert.Equal(t, 0, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, "waiting for resource")
			require.NotNil(t, got.Status.HealthCheckExpiry)
			assert.WithinDuration(t, time.Now().Add(time.Minute), got.Status.HealthCheckExpiry.Time, 10*time.Second)
			_, err := getBundle(c, "b1-rollback-alarm")
			assert.True(t, apierrors.IsNotFound(err), "no rollback Bundle before the timeout")

			// health.timeout passed with the same status: rolled back.
			expired := metav1.NewTime(time.Now().Add(-time.Second))
			c, got, _ = healthCase{env: env, images: images, objs: objs(),
				status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired, Message: got.Status.Message}}.run(t)
			assert.Equal(t, "RollingBack", got.Status.State, got.Status.Message)
			assert.Equal(t, 1, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, "health check timeout after 1m0s; last result: waiting for resource")
			assert.Contains(t, got.Status.Message, "rollback Bundle b1-rollback-alarm created")
			rb, err := getBundle(c, "b1-rollback-alarm")
			require.NoError(t, err)
			assert.Equal(t, "true", rb.Labels["kardinal.io/rollback"])
			assert.Equal(t, "b1", rb.Annotations["kardinal.io/rollback-from"])
			require.NotNil(t, rb.Spec.Provenance)
			assert.Equal(t, "b0", rb.Spec.Provenance.RollbackOf, "the target is the Bundle verified before")
			assert.Equal(t, []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}, rb.Spec.Images,
				"the rollback restores the previous Verified images, never the failing one")
			require.NotNil(t, rb.Spec.Intent)
			assert.Equal(t, "test", rb.Spec.Intent.TargetEnvironment)
			assert.Equal(t, []string{"RollbackStarted"}, auditActions(t, c))
		})
	}
}

// TestHealthTimeoutAppliesOnHealthFailure proves that reaching health.timeout
// is counted in status.consecutiveHealthFailures (which the RollbackPolicy
// reconciler reads) and applies each onHealthFailure action; that
// onHealthFailure=rollback stops the step for a human when nothing was
// verified before; and that a rollback Bundle is not rolled back again, so
// rollbacks do not chain.
func TestHealthTimeoutAppliesOnHealthFailure(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2-bad"}}
	expired := metav1.NewTime(time.Now().Add(-time.Second))
	tests := []struct {
		name            string
		onHealthFailure string
		noHistory       bool
		bundle          func(*v1alpha1.Bundle)
		wantState       string
		wantMsg         string
		wantRollback    bool
		wantAudit       []string
	}{
		{onHealthFailure: "", wantState: "Failed",
			wantMsg: "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s", wantAudit: []string{"PromotionFailed"}},
		{onHealthFailure: "none", wantState: "Failed",
			wantMsg: "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s", wantAudit: []string{"PromotionFailed"}},
		{onHealthFailure: "abort", wantState: "AbortedByAlarm",
			wantMsg: "health alarm via resource (onHealthFailure=abort): health check timeout after 1m0s", wantAudit: []string{"PromotionFailed"}},
		{onHealthFailure: "rollback", wantState: "RollingBack",
			wantMsg: "health alarm via resource (onHealthFailure=rollback): health check timeout after 1m0s", wantRollback: true,
			wantAudit: []string{"RollbackStarted"}},
		{name: "rollback with nothing verified before stops for a human", onHealthFailure: "rollback", noHistory: true,
			wantState: "AbortedByAlarm", wantMsg: "no automatic rollback", wantAudit: []string{"PromotionFailed"}},
		{name: "a rollback Bundle is not rolled back again", onHealthFailure: "rollback",
			bundle: func(b *v1alpha1.Bundle) { // as lifecycle.PlanRollback builds it
				b.Labels = map[string]string{"kardinal.io/rollback": "true", "kardinal.io/pipeline": "p"}
				b.Annotations = map[string]string{"kardinal.io/rollback-from": "b-bad"}
				b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "b0"}
			},
			wantState: "AbortedByAlarm", wantMsg: "Bundle b1 is a rollback and is not rolled back again", wantAudit: []string{"PromotionFailed"}},
		{name: "provenance.rollbackOf alone marks a rollback Bundle", onHealthFailure: "rollback",
			bundle:    func(b *v1alpha1.Bundle) { b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "b0"} },
			wantState: "AbortedByAlarm", wantMsg: "Bundle b1 is a rollback and is not rolled back again", wantAudit: []string{"PromotionFailed"}},
	}
	for _, tt := range tests {
		name := tt.name
		if name == "" {
			name = "onHealthFailure=" + tt.onHealthFailure
		}
		t.Run(name, func(t *testing.T) {
			env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
				OnHealthFailure: tt.onHealthFailure}
			objs := []client.Object{crashLoopDeployment("p", "test", appsv1.RecreateDeploymentStrategyType)}
			if !tt.noHistory {
				objs = append(objs, verifiedBefore("test")...)
			}
			hc := healthCase{env: env, images: images, objs: objs, bundle: tt.bundle,
				status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired}}
			c, got, _ := hc.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, 1, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			_, err := getBundle(c, "b1-rollback-alarm")
			assert.Equal(t, tt.wantRollback, err == nil, "rollback Bundle created")
			assert.Equal(t, tt.wantAudit, auditActions(t, c))
		})
	}
}

// TestHealthTimeoutChecksOnceMore: a step reconciled after its
// health.timeout passed (the controller was down or behind for longer than
// the timeout) checks the environment once more before it applies
// onHealthFailure, also when its last check was a moment ago. An environment
// that became healthy in time is Verified, not failed and rolled back on the
// result read before; one still not updated fails with this check's result.
func TestHealthTimeoutChecksOnceMore(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
		OnHealthFailure: "rollback"}
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	expired := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	checked := metav1.NewTime(time.Now().Add(-time.Second))
	stale := "waiting for resource: Deployment test/p not updated yet: runs ghcr.io/org/app:v1, Bundle has ghcr.io/org/app:v2"
	status := v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired, LastHealthCheckAt: &checked, Message: stale}
	for _, tc := range []struct {
		name, image, state, msg string
	}{
		{"healthy by now", "ghcr.io/org/app:v2", "Verified", "health check passed via resource"},
		{"still not updated", "ghcr.io/org/app:v1", "RollingBack", "health check timeout after 1m0s; last result: waiting for resource: Deployment test/p not updated yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := append(verifiedBefore("test"), withImage(healthyDeployment("p", "test"), tc.image))
			c, got, _ := healthCase{env: env, images: images, objs: objs, status: *status.DeepCopy()}.run(t)
			assert.Equal(t, tc.state, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tc.msg)
			_, err := getBundle(c, "b1-rollback-alarm")
			assert.Equal(t, tc.state == "Verified", apierrors.IsNotFound(err), "a rollback Bundle only when it is still not updated: %v", err)
		})
	}
}

// TestHealthTimeoutLateBakeStarts: with a bake, a step reconciled past its
// health.timeout but before the bake's deadline checks once more too, and an
// environment healthy by now starts its bake window instead of failing.
func TestHealthTimeoutLateBakeStarts(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
		OnHealthFailure: "rollback", Bake: &v1alpha1.BakeConfig{Minutes: 30}}
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	expired := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	status := v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired,
		Message: "waiting for resource: Deployment test/p not updated yet"}
	objs := append(verifiedBefore("test"), withImage(healthyDeployment("p", "test"), "ghcr.io/org/app:v2"))
	c, got, _ := healthCase{env: env, images: images, objs: objs, status: status}.run(t)
	assert.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
	assert.NotNil(t, got.Status.BakeStartedAt, "the bake window starts")
	_, err := getBundle(c, "b1-rollback-alarm")
	assert.True(t, apierrors.IsNotFound(err), "no rollback: %v", err)
}

// TestHealthTimeoutLateAdapterError: a late check whose adapter fails is
// tried once more (status.lastHealthCheckAt past the expiry marks it) before
// onHealthFailure applies on the result read before, with the error.
func TestHealthTimeoutLateAdapterError(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
		OnHealthFailure: "rollback"}
	pipeline := makePipeline("p")
	pipeline.Spec.Environments = []v1alpha1.EnvironmentSpec{env}
	bundle := makeBundle("b1", "p")
	bundle.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	ps := labelled(makeStep("step", "p", "b1", "test"))
	expired := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	ps.Status = v1alpha1.PromotionStepStatus{State: "HealthChecking", Steps: recordedSteps(""), HealthCheckExpiry: &expired,
		Message: "waiting for resource: Deployment test/p not updated yet"}
	base := newClient(t, append([]client.Object{pipeline, bundle, ps}, verifiedBefore("test")...)...)
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return errors.New("etcdserver: request timed out")
			}
			return cl.Get(ctx, key, obj, opts...)
		}})
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme()))}

	res, err := r.Reconcile(context.Background(), reqFor("step"))
	require.NoError(t, err)
	got := getStep(t, c, "step")
	assert.Equal(t, "HealthChecking", got.Status.State, "the first late error is tried again: %s", got.Status.Message)
	assert.Positive(t, res.RequeueAfter)
	require.NotNil(t, got.Status.LastHealthCheckAt)
	assert.True(t, got.Status.LastHealthCheckAt.After(expired.Time))

	_, err = r.Reconcile(context.Background(), reqFor("step"))
	require.NoError(t, err)
	got = getStep(t, c, "step")
	assert.Equal(t, "RollingBack", got.Status.State, got.Status.Message)
	assert.Contains(t, got.Status.Message, "last result: waiting for resource: Deployment test/p not updated yet")
	assert.Contains(t, got.Status.Message, "the health check past the timeout failed: get deployment test/p: etcdserver: request timed out")
}

// TestRollbackPerEnvironment (#2082): when two environments of one Bundle
// fail at once with onHealthFailure=rollback (fan-out, waves, a fleet's
// targets in flight together), each gets its own rollback Bundle; neither
// takes the other's as its own. A step reconciled again reuses its own.
func TestRollbackPerEnvironment(t *testing.T) {
	envs := []string{"prod-eu", "prod-us"}
	c := newClient(t, failingEnvironments(envs...)...)
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme()))}
	for _, e := range append(envs, envs...) {
		_, err := r.Reconcile(context.Background(), reqFor("step-"+e))
		require.NoError(t, err)
	}
	targets := rollbackTargets(t, c)
	assert.Equal(t, map[string]string{"prod-eu": "b1-rollback-alarm", "prod-us": "b1-rollback-alarm-prod-us"}, targets,
		"one rollback Bundle per failing environment")
	for _, e := range envs {
		got := getStep(t, c, "step-"+e)
		assert.Equal(t, "RollingBack", got.Status.State, got.Status.Message)
		assert.Contains(t, got.Status.Message, "rollback Bundle "+targets[e]+" created", "%s names its own rollback", e)
	}
}

// TestRollbackPerEnvironmentRace (#2082 QA): a step that reads the plain
// name as free (a cache behind another environment's create) and then
// finds it taken by that environment's rollback does not take it as its
// own: it fails the reconcile, and the retry creates its own.
func TestRollbackPerEnvironmentRace(t *testing.T) {
	envs := []string{"prod-eu", "prod-us"}
	base := newClient(t, failingEnvironments(envs...)...)
	stale := 0
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1alpha1.Bundle); ok && key.Name == "b1-rollback-alarm" && stale == 1 {
				stale++
				return apierrors.NewNotFound(v1alpha1.GroupVersion.WithResource("bundles").GroupResource(), key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		}})
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme()))}
	_, err := r.Reconcile(context.Background(), reqFor("step-prod-eu"))
	require.NoError(t, err)
	stale = 1 // prod-us's next read of the name misses prod-eu's rollback
	_, err = r.Reconcile(context.Background(), reqFor("step-prod-us"))
	require.Error(t, err, "the name is another environment's: retry")
	assert.Equal(t, 2, stale, "the stale read happened")
	assert.NotEqual(t, "RollingBack", getStep(t, c, "step-prod-us").Status.State, "prod-us did not take prod-eu's rollback")
	_, err = r.Reconcile(context.Background(), reqFor("step-prod-us"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"prod-eu": "b1-rollback-alarm", "prod-us": "b1-rollback-alarm-prod-us"}, rollbackTargets(t, c))
	assert.Contains(t, getStep(t, c, "step-prod-us").Status.Message, "rollback Bundle b1-rollback-alarm-prod-us created")
}

// rollbackTargets maps each rollback Bundle's target environment to it.
func rollbackTargets(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	targets := map[string]string{}
	for _, b := range bundles.Items {
		if b.Labels["kardinal.io/rollback"] == "true" {
			require.NotNil(t, b.Spec.Intent, b.Name)
			targets[b.Spec.Intent.TargetEnvironment] = b.Name
		}
	}
	return targets
}

// failingEnvironments is Pipeline p with environments envs, where b0 was
// Verified and b1 crash-loops past its health.timeout with
// onHealthFailure=rollback, step step-<env> each.
func failingEnvironments(envs ...string) []client.Object {
	pipeline := makePipeline("p")
	pipeline.Spec.Environments = nil
	objs := []client.Object{pipeline}
	for _, e := range envs {
		pipeline.Spec.Environments = append(pipeline.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e,
			Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"}, OnHealthFailure: "rollback"})
		objs = append(objs, crashLoopDeployment("p", e, appsv1.RecreateDeploymentStrategyType))
		s0 := labelled(makeStep("step-b0-"+e, "p", "b0", e))
		s0.Status = v1alpha1.PromotionStepStatus{State: "Verified", Conditions: []metav1.Condition{{
			Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified",
			LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour))}}}
		expired := metav1.NewTime(time.Now().Add(-time.Second))
		ps := labelled(makeStep("step-"+e, "p", "b1", e))
		ps.Status = v1alpha1.PromotionStepStatus{State: "HealthChecking", Steps: recordedSteps(""), HealthCheckExpiry: &expired}
		objs = append(objs, s0, ps)
	}
	b0 := makeBundle("b0", "p")
	b0.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}
	b0.Status.Phase = "Superseded"
	b1 := makeBundle("b1", "p")
	b1.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2-bad"}}
	return append(objs, b0, b1)
}

// TestRollbackFindsAnEarlierRollback: a rollback of the failing Bundle in
// the environment that exists already, under a name of another scheme (one
// a v0.11 controller gave a long name), is found by its labels, annotation
// and intent and reused: the step names it and nothing more is created.
func TestRollbackFindsAnEarlierRollback(t *testing.T) {
	legacy := makeBundle("b1-rollback-alarm-0a1b2c3d", "p")
	legacy.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "p"}
	legacy.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "b1", lifecycle.AnnotationCreatedBy: lifecycle.ControllerCreator}
	legacy.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: lifecycle.ControllerCreator, Via: v1alpha1.ViaAutoRollback}
	legacy.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-eu"}
	c := newClient(t, append(failingEnvironments("prod-eu"), legacy)...)
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme()))}
	_, err := r.Reconcile(context.Background(), reqFor("step-prod-eu"))
	require.NoError(t, err)
	got := getStep(t, c, "step-prod-eu")
	assert.Equal(t, "RollingBack", got.Status.State, got.Status.Message)
	assert.Contains(t, got.Status.Message, "rollback Bundle b1-rollback-alarm-0a1b2c3d created")
	assert.Equal(t, map[string]string{"prod-eu": "b1-rollback-alarm-0a1b2c3d"}, rollbackTargets(t, c), "no second rollback")
}
