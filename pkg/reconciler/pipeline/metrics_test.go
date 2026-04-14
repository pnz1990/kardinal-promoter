// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// --- helpers ---

func makePipelineWithEnvs(name, ns string, envs ...string) *kardinalv1alpha1.Pipeline {
	envSpecs := make([]kardinalv1alpha1.EnvironmentSpec, len(envs))
	for i, e := range envs {
		envSpecs[i] = kardinalv1alpha1.EnvironmentSpec{Name: e}
	}
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envSpecs},
	}
}

func makeVerifiedBundle(name, ns, pipelineName string, createdAt time.Time) *kardinalv1alpha1.Bundle {
	return &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipelineName,
		},
	}
}

func makeVerifiedStep(bundleName, pipelineName, env, ns string, verifiedAt time.Time) *kardinalv1alpha1.PromotionStep {
	return &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              bundleName + "-" + env,
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(verifiedAt.Add(-10 * time.Minute)),
			Labels: map[string]string{
				"kardinal.io/bundle":   bundleName,
				"kardinal.io/pipeline": pipelineName,
			},
		},
		Spec: kardinalv1alpha1.PromotionStepSpec{
			BundleName:   bundleName,
			PipelineName: pipelineName,
			Environment:  env,
		},
		Status: kardinalv1alpha1.PromotionStepStatus{
			State: "Verified",
			Conditions: []metav1.Condition{
				{
					Type:               "Verified",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(verifiedAt),
				},
			},
		},
	}
}

// --- ComputeDeploymentMetrics unit tests ---

func TestComputeDeploymentMetrics_NilWhenNoVerifiedBundles(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	result := pipeline.ComputeDeploymentMetrics(p, nil, nil, time.Now())
	assert.Nil(t, result, "must return nil when no Verified bundles exist")
}

func TestComputeDeploymentMetrics_BasicLeadTime(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	p := makePipelineWithEnvs("app", "default", "test", "prod")

	// Bundle created 2 hours before Verified in prod.
	b := makeVerifiedBundle("app-v1", "default", "app", now.Add(-2*time.Hour))
	step := makeVerifiedStep("app-v1", "app", "prod", "default", now.Add(-10*time.Minute))

	result := pipeline.ComputeDeploymentMetrics(p, []kardinalv1alpha1.Bundle{*b},
		[]kardinalv1alpha1.PromotionStep{*step}, now)

	require.NotNil(t, result)
	assert.Equal(t, 1, result.SampleSize)
	assert.Equal(t, 1, result.RolloutsLast30Days, "should count one rollout in last 30 days")
	assert.Greater(t, result.P50CommitToProdMinutes, int64(0), "lead time must be > 0")
	// Lead time = verifiedAt - createdAt = (now-10min) - (now-2h) = ~110 minutes
	assert.InDelta(t, 110, result.P50CommitToProdMinutes, 2, "p50 ≈ 110 minutes")
	assert.Equal(t, result.P50CommitToProdMinutes, result.P90CommitToProdMinutes,
		"p50==p90 with single sample")
	assert.Equal(t, 0, result.StaleProdDays, "promoted ~10min ago → 0 stale days")
}

func TestComputeDeploymentMetrics_Percentiles(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	p := makePipelineWithEnvs("app", "default", "test", "prod")

	// 10 bundles with lead times: 10, 20, 30, ..., 100 minutes.
	bundles := make([]kardinalv1alpha1.Bundle, 10)
	steps := make([]kardinalv1alpha1.PromotionStep, 10)
	for i := 0; i < 10; i++ {
		leadMins := time.Duration(i+1) * 10 * time.Minute
		createdAt := now.Add(-leadMins - 5*time.Minute)
		verifiedAt := now.Add(-5 * time.Minute)
		name := "app-v" + string(rune('1'+i))
		b := makeVerifiedBundle(name, "default", "app", createdAt)
		s := makeVerifiedStep(name, "app", "prod", "default", verifiedAt)
		bundles[i] = *b
		steps[i] = *s
	}

	result := pipeline.ComputeDeploymentMetrics(p, bundles, steps, now)
	require.NotNil(t, result)
	assert.Equal(t, 10, result.SampleSize)
	// All verified at same time, but lead times are different per bundle.
	// Sorted: 10,20,30,40,50,60,70,80,90,100 minutes
	// p50 index = 10*50/100 = 5 → value at index 5 = 60
	// p90 index = 10*90/100 = 9 → value at index 9 = 100
	assert.Equal(t, int64(60), result.P50CommitToProdMinutes, "p50 should be 60 min")
	assert.Equal(t, int64(100), result.P90CommitToProdMinutes, "p90 should be 100 min")
}

func TestComputeDeploymentMetrics_StaleProdDays(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	p := makePipelineWithEnvs("app", "default", "prod")

	// Last promotion was 5 days ago.
	verifiedAt := now.Add(-5 * 24 * time.Hour)
	b := makeVerifiedBundle("app-v1", "default", "app", verifiedAt.Add(-1*time.Hour))
	s := makeVerifiedStep("app-v1", "app", "prod", "default", verifiedAt)

	result := pipeline.ComputeDeploymentMetrics(p, []kardinalv1alpha1.Bundle{*b},
		[]kardinalv1alpha1.PromotionStep{*s}, now)

	require.NotNil(t, result)
	assert.Equal(t, 5, result.StaleProdDays, "stale prod = 5 days")
}

func TestComputeDeploymentMetrics_RollbackRateMillis(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	p := makePipelineWithEnvs("app", "default", "prod")

	// 4 bundles: 1 is a rollback (25% → 250 millis)
	rollbackBundle := makeVerifiedBundle("app-v2", "default", "app", now.Add(-1*time.Hour))
	rollbackBundle.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{RollbackOf: "app-v1"}

	bundles := []kardinalv1alpha1.Bundle{
		*makeVerifiedBundle("app-v1", "default", "app", now.Add(-4*time.Hour)),
		*rollbackBundle,
		*makeVerifiedBundle("app-v3", "default", "app", now.Add(-2*time.Hour)),
		*makeVerifiedBundle("app-v4", "default", "app", now.Add(-3*time.Hour)),
	}
	steps := []kardinalv1alpha1.PromotionStep{
		*makeVerifiedStep("app-v1", "app", "prod", "default", now.Add(-3*time.Hour+30*time.Minute)),
		*makeVerifiedStep("app-v2", "app", "prod", "default", now.Add(-30*time.Minute)),
		*makeVerifiedStep("app-v3", "app", "prod", "default", now.Add(-90*time.Minute)),
		*makeVerifiedStep("app-v4", "app", "prod", "default", now.Add(-2*time.Hour+30*time.Minute)),
	}

	result := pipeline.ComputeDeploymentMetrics(p, bundles, steps, now)
	require.NotNil(t, result)
	assert.Equal(t, 4, result.SampleSize)
	// 1/4 = 250 millis
	assert.Equal(t, 250, result.AutoRollbackRateMillis, "1/4 bundles are rollbacks = 250 millis")
}

func TestComputeDeploymentMetrics_BundlesForOtherPipelineExcluded(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	p := makePipelineWithEnvs("app-a", "default", "prod")

	// Bundle for app-b should be excluded.
	bA := makeVerifiedBundle("app-a-v1", "default", "app-a", now.Add(-1*time.Hour))
	bB := makeVerifiedBundle("app-b-v1", "default", "app-b", now.Add(-1*time.Hour))
	sA := makeVerifiedStep("app-a-v1", "app-a", "prod", "default", now.Add(-30*time.Minute))
	sB := makeVerifiedStep("app-b-v1", "app-b", "prod", "default", now.Add(-30*time.Minute))

	result := pipeline.ComputeDeploymentMetrics(p,
		[]kardinalv1alpha1.Bundle{*bA, *bB},
		[]kardinalv1alpha1.PromotionStep{*sA, *sB},
		now)

	require.NotNil(t, result)
	assert.Equal(t, 1, result.SampleSize, "only app-a bundles should be counted")
}

func TestComputeDeploymentMetrics_ComputedAtIsSet(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	p := makePipelineWithEnvs("app", "default", "prod")
	b := makeVerifiedBundle("app-v1", "default", "app", now.Add(-1*time.Hour))
	s := makeVerifiedStep("app-v1", "app", "prod", "default", now.Add(-30*time.Minute))

	result := pipeline.ComputeDeploymentMetrics(p, []kardinalv1alpha1.Bundle{*b},
		[]kardinalv1alpha1.PromotionStep{*s}, now)

	require.NotNil(t, result)
	require.NotNil(t, result.ComputedAt)
	assert.Equal(t, now.UTC(), result.ComputedAt.UTC())
}

// --- PipelineReconciler integration tests for deploymentMetrics ---

func newPipelineScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// newClientWithIndex builds a fake client that supports the spec.pipelineName
// field selector used by PipelineReconciler to list PromotionSteps.
func newClientWithIndex(scheme *runtime.Scheme, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Pipeline{}).
		WithIndex(&kardinalv1alpha1.PromotionStep{}, "spec.pipelineName",
			func(obj client.Object) []string {
				s, ok := obj.(*kardinalv1alpha1.PromotionStep)
				if !ok {
					return nil
				}
				return []string{s.Spec.PipelineName}
			},
		).
		Build()
}

func TestPipelineReconciler_SetsDeploymentMetrics(t *testing.T) {
	now := time.Now().UTC()
	ns := "default"

	p := makePipelineWithEnvs("my-app", ns, "test", "prod")

	b := makeVerifiedBundle("my-app-v1", ns, "my-app", now.Add(-2*time.Hour))
	b.Status = kardinalv1alpha1.BundleStatus{Phase: "Verified"}
	s := makeVerifiedStep("my-app-v1", "my-app", "prod", ns, now.Add(-30*time.Minute))

	scheme := newPipelineScheme()
	c := newClientWithIndex(scheme, p, b, s)

	r := &pipeline.Reconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app", Namespace: ns}}
	_, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(t.Context(), req.NamespacedName, &got))

	require.NotNil(t, got.Status.DeploymentMetrics,
		"PipelineReconciler must write DeploymentMetrics after a Verified bundle exists")
	assert.Equal(t, 1, got.Status.DeploymentMetrics.SampleSize)
	assert.Greater(t, got.Status.DeploymentMetrics.P50CommitToProdMinutes, int64(0))
	assert.NotNil(t, got.Status.DeploymentMetrics.ComputedAt)
}

func TestPipelineReconciler_NilMetricsWhenNoBundles(t *testing.T) {
	ns := "default"
	p := makePipelineWithEnvs("empty-app", ns, "prod")

	scheme := newPipelineScheme()
	c := newClientWithIndex(scheme, p)

	r := &pipeline.Reconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "empty-app", Namespace: ns}}
	_, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(t.Context(), req.NamespacedName, &got))

	assert.Nil(t, got.Status.DeploymentMetrics,
		"DeploymentMetrics must be nil when no bundles exist")
}
