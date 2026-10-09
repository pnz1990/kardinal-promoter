// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// retired marks b retired with records of steps, as the Bundle reconciler
// does before it deletes the Graph (#1492).
func retired(b *v1alpha1.Bundle, steps ...*v1alpha1.PromotionStep) *v1alpha1.Bundle {
	b.Status.Conditions = append(b.Status.Conditions, metav1.Condition{
		Type: lifecycle.ConditionGraphRetired, Status: metav1.ConditionTrue, Reason: "Retired"})
	b.Status.RetiredAt = &metav1.Time{Time: t0}
	for _, s := range steps {
		b.Status.RetiredSteps = append(b.Status.RetiredSteps, lifecycle.RetiredStepOf(s))
	}
	return b
}

// TestRetiredStep_RoundTrip checks that the step rebuilt from a record keeps
// what rollback, promote, history, metrics and the CLI read: labels, spec,
// creation time, state, message, PR URL, health check expiry and Verified
// time.
func TestRetiredStep_RoundTrip(t *testing.T) {
	s := step("v1", "app", "prod", "Verified", 3)
	s.Spec.StepType = "pr-review"
	s.Status.Message = strings.Repeat("é", 300) // 600 bytes
	s.Status.Outputs = map[string]string{"prURL": "https://git.example/pr/7"}
	expiry := metav1.NewTime(t0.Add(4 * time.Minute))
	s.Status.HealthCheckExpiry = &expiry
	b := bundle("v1", "app", "1", 0)

	got := lifecycle.StepFromRetired(b, lifecycle.RetiredStepOf(s))
	assert.Equal(t, s.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
	assert.Equal(t, map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "v1",
		"kardinal.io/environment": "prod", lifecycle.LabelRetired: "true"}, got.Labels)
	assert.Equal(t, v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "v1", Environment: "prod",
		StepType: "pr-review"}, got.Spec)
	assert.True(t, got.CreationTimestamp.Equal(&s.CreationTimestamp))
	assert.Equal(t, "Verified", got.Status.State)
	assert.Equal(t, "https://git.example/pr/7", got.Status.PRURL, "the PR URL output is kept")
	assert.Len(t, got.Status.Message, 512, "the message is cut on a character boundary")
	assert.True(t, strings.HasPrefix(s.Status.Message, got.Status.Message))
	require.NotNil(t, got.Status.HealthCheckExpiry)
	assert.True(t, got.Status.HealthCheckExpiry.Equal(&expiry))
	at, ok := lifecycle.VerifiedTime(&got)
	require.True(t, ok)
	assert.Equal(t, t0.Add(3*time.Minute), at)

	// A step that never became Verified has no Verified condition.
	failed := lifecycle.StepFromRetired(b, lifecycle.RetiredStepOf(step("v1", "app", "test", "Failed", 1)))
	_, ok = lifecycle.VerifiedTime(&failed)
	assert.False(t, ok)
}

// TestAddRetiredSteps checks the merge: records of retired Bundles that match
// the selector are added; a record whose step still exists, a Bundle that is
// not retired, and a record of another pipeline are not.
func TestAddRetiredSteps(t *testing.T) {
	live := *step("v2", "app", "prod", "Verified", 11)
	bundles := []v1alpha1.Bundle{
		*retired(bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1), step("v1", "app", "test", "Verified", 1)),
		*retired(bundle("v2", "app", "2", 10), &live), // kro has not deleted it yet
		*bundle("v3", "app", "3", 20),
		*retired(bundle("o1", "other", "1", 0), step("o1", "other", "prod", "Verified", 1)),
	}
	bundles[2].Status.RetiredSteps = []v1alpha1.RetiredStep{{Name: "not-retired"}}

	got := lifecycle.AddRetiredSteps([]v1alpha1.PromotionStep{live}, bundles,
		map[string]string{lifecycle.LabelPipeline: "app"})
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"v2-prod", "v1-prod", "v1-test"}, names)

	all := lifecycle.AddRetiredSteps(nil, bundles, nil)
	assert.Len(t, all, 4, "a nil selector matches every record")
}

// TestPlanRollback_RetiredHistory checks that rollback and DeployedBundle
// work when the Bundles involved were retired and only their records are
// left (#1492).
func TestPlanRollback_RetiredHistory(t *testing.T) {
	tests := []struct {
		name string
		objs []client.Object
	}{
		{name: "both retired", objs: []client.Object{
			retired(bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1)),
			retired(bundle("v2", "app", "2", 10), step("v2", "app", "prod", "Verified", 11)),
		}},
		{name: "the target retired, the deployed one live", objs: []client.Object{
			retired(bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1)),
			bundle("v2", "app", "2", 10), step("v2", "app", "prod", "Verified", 11),
		}},
		{name: "retired while kro still deletes its steps", objs: []client.Object{
			retired(bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1)), step("v1", "app", "prod", "Verified", 1),
			retired(bundle("v2", "app", "2", 10), step("v2", "app", "prod", "Verified", 11)),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, append(tc.objs, pipeline("app", "test", "prod"))...)
			plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
				Namespace: ns, Pipeline: "app", Environment: "prod", Actor: "alice", Now: t0.Add(time.Hour)})
			require.NoError(t, err)
			assert.Equal(t, "v1", plan.Target.Name)
			assert.Equal(t, "v2", plan.Bundle.Annotations[lifecycle.AnnotationRollbackFrom])

			steps, err := lifecycle.ListPromotionSteps(context.Background(), c, ns,
				client.MatchingLabels{lifecycle.LabelPipeline: "app"})
			require.NoError(t, err)
			assert.Len(t, steps, 2, "each step once")
			assert.Equal(t, "v2", lifecycle.DeployedBundle(steps, "app", "prod"))

			one, err := lifecycle.ListPromotionSteps(context.Background(), c, ns,
				client.MatchingLabels{lifecycle.LabelBundle: "v1"})
			require.NoError(t, err)
			require.Len(t, one, 1)
			assert.Equal(t, "v1", one[0].Spec.BundleName)
		})
	}
}

// TestRetired_ReadsRetiredAt checks that only status.retiredAt makes a Bundle
// retired: a GraphRetired condition alone does not, and a Bundle whose
// condition another writer dropped still is.
func TestRetired_ReadsRetiredAt(t *testing.T) {
	b := bundle("v1", "app", "1", 0)
	b.Status.Conditions = []metav1.Condition{{Type: lifecycle.ConditionGraphRetired, Status: metav1.ConditionTrue}}
	assert.False(t, lifecycle.Retired(b))
	b.Status.Conditions = nil
	b.Status.RetiredAt = &metav1.Time{Time: t0}
	assert.True(t, lifecycle.Retired(b))
}

// TestRetiredStep_PRURLCut checks that a PR URL longer than the CRD allows
// (2048) is cut, so the status write is not refused.
func TestRetiredStep_PRURLCut(t *testing.T) {
	s := step("v1", "app", "prod", "Verified", 1)
	s.Status.PRURL = "https://git.example/" + strings.Repeat("p", 3000)
	assert.Len(t, lifecycle.RetiredStepOf(s).PRURL, 2048)
}

// TestRetiredStep_MarkerDigest: a retired step keeps the marker digest of
// its render (status.outputs.markerDigest), so later renders of the
// environment still know it once its RenderRun is gone; a value that is not
// a sha256 digest is dropped, as the CRD allows 64 characters.
func TestRetiredStep_MarkerDigest(t *testing.T) {
	for _, tc := range []struct {
		name, out, want string
	}{
		{name: "digest", out: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{name: "none"},
		{name: "not a digest", out: strings.Repeat("a", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := step("v1", "app", "prod", "Verified", 1)
			s.Status.Outputs = map[string]string{"markerDigest": tc.out}
			assert.Equal(t, tc.want, lifecycle.RetiredStepOf(s).MarkerDigest)
		})
	}
}
