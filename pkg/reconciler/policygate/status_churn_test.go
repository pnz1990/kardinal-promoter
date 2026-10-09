// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPolicyGateReconciler_SkipsUnchangedWrites checks that a re-evaluation
// with the same result writes nothing (no new resourceVersion), so it does
// not wake kro and the gate's watchers, unless something waits for a newer
// result: a PromotionStep that has not started, requires the gate and was
// created after the stored result, a spec change, or statusHeartbeat (10m)
// since the last write.
func TestPolicyGateReconciler_SkipsUnchangedWrites(t *testing.T) {
	mon := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC) // a weekday: the gate passes
	step := func(name, state string, created time.Time, gates ...string) *kardinalv1alpha1.PromotionStep {
		return &kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
				Labels:            map[string]string{"kardinal.io/bundle": "nginx-demo-v1"},
				CreationTimestamp: metav1.NewTime(created)},
			Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "prod", RequiredGates: gates},
			Status: kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	tests := []struct {
		name      string
		at        time.Time
		step      *kardinalv1alpha1.PromotionStep
		editSpec  bool
		wantWrite bool
	}{
		{name: "same result, nothing waits", at: mon.Add(time.Minute)},
		{name: "same result, heartbeat passed", at: mon.Add(10 * time.Minute), wantWrite: true},
		{name: "new step waits for a fresh result", at: mon.Add(time.Minute),
			step: step("prod", "", mon.Add(30*time.Second), "no-weekend"), wantWrite: true},
		{name: "pending step waits for a fresh result", at: mon.Add(time.Minute),
			step: step("prod", "Pending", mon.Add(30*time.Second), "no-weekend"), wantWrite: true},
		{name: "step created before the result", at: mon.Add(time.Minute),
			step: step("prod", "", mon.Add(-time.Minute), "no-weekend")},
		{name: "started step", at: mon.Add(time.Minute),
			step: step("prod", "Promoting", mon.Add(30*time.Second), "no-weekend")},
		{name: "step requires another gate", at: mon.Add(time.Minute),
			step: step("prod", "", mon.Add(30*time.Second), "other")},
		{name: "spec changed", at: mon.Add(time.Minute), editSpec: true, wantWrite: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gc := newGateClock(t)
			first := gc.eval(mon)
			require.True(t, first.Status.Ready)
			ctx := context.Background()
			if tc.step != nil {
				require.NoError(t, gc.c.Create(ctx, tc.step))
			}
			if tc.editSpec {
				// The API server bumps metadata.generation on a spec change; the
				// fake client does not.
				first.Spec.Message = "edited"
				first.Generation++
				require.NoError(t, gc.c.Update(ctx, &first))
			}
			var before kardinalv1alpha1.PolicyGate
			require.NoError(t, gc.c.Get(ctx, types.NamespacedName{Name: "no-weekend", Namespace: "default"}, &before))

			got := gc.eval(tc.at)
			assert.True(t, got.Status.Ready)
			if tc.wantWrite {
				assert.NotEqual(t, before.ResourceVersion, got.ResourceVersion, "status written")
				assert.True(t, tc.at.Equal(got.Status.LastEvaluatedAt.Time), "lastEvaluatedAt %s", got.Status.LastEvaluatedAt)
			} else {
				assert.Equal(t, before.ResourceVersion, got.ResourceVersion, "nothing written")
				assert.True(t, mon.Equal(got.Status.LastEvaluatedAt.Time), "lastEvaluatedAt %s", got.Status.LastEvaluatedAt)
			}
			// Idempotent: evaluating again at the same time writes nothing more.
			again := gc.eval(tc.at)
			assert.Equal(t, got.ResourceVersion, again.ResourceVersion, "a second evaluation writes nothing")
		})
	}
}
