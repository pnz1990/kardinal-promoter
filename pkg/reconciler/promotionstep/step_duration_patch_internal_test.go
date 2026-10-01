// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestStepDuration_ObservedOnlyAfterThePatch: a step closed in memory is
// observed in kardinal_step_duration_seconds only once the status patch that
// closes it succeeds. A failed patch is retried by a later reconcile, which
// closes the same steps again from the unchanged object, so each step is
// observed once in all; a step deleted while reconciling is not observed.
// Before, the steps were observed when closed, before the patch, so a failed
// and retried patch counted them twice.
func TestStepDuration_ObservedOnlyAfterThePatch(t *testing.T) {
	ago := func(d time.Duration) *metav1.Time { mt := metav1.NewTime(time.Now().Add(-d)); return &mt }
	retryable := fmt.Errorf("step git-push: %w", context.DeadlineExceeded)
	tests := []struct {
		name     string
		state    string
		steps    func(seq []string) []v1alpha1.StepStatus
		index    int
		reconcil func(r *Reconciler, base, ps *v1alpha1.PromotionStep, seq []string) error
		want     []uint64 // samples per step after the run
	}{
		{name: "the merge closes wait-for-merge (transition)", state: StateWaitingForMerge,
			steps: func(seq []string) []v1alpha1.StepStatus {
				return []v1alpha1.StepStatus{
					{Name: seq[0], State: v1alpha1.StepExecutionCompleted, StartedAt: ago(3 * time.Minute), CompletedAt: ago(2 * time.Minute)},
					{Name: seq[1], State: v1alpha1.StepExecutionInProgress, StartedAt: ago(2 * time.Minute)},
					{Name: healthCheckStep, State: v1alpha1.StepExecutionCompleted},
				}
			},
			reconcil: func(r *Reconciler, base, ps *v1alpha1.PromotionStep, _ []string) error {
				return r.transition(context.Background(), base, ps, StateHealthChecking, "PR #5 merged")
			},
			want: []uint64{0, 1}},
		{name: "the engine closes steps, then the step waits for its PR (transition)", state: StatePromoting,
			steps: func(seq []string) []v1alpha1.StepStatus { return initStepStatuses(append(seq, healthCheckStep)) },
			reconcil: func(r *Reconciler, base, ps *v1alpha1.PromotionStep, seq []string) error {
				now := time.Now()
				closed := updateStepStatuses(ps, append(seq, healthCheckStep), 1, false, "", map[int]steps.StepTiming{
					0: {Started: now.Add(-2 * time.Second), Finished: now.Add(-time.Second)},
					1: {Started: now.Add(-time.Second), Finished: now},
				})
				ps.Status.Outputs = map[string]string{"prURL": "https://example.com/pr/5"}
				_, err := r.transitionClosing(context.Background(), base, ps, StateWaitingForMerge, "waiting", "", closed)
				return err
			},
			want: []uint64{1, 0}},
		{name: "a step error is retried after the engine closed steps (plain patch)", state: StatePromoting, index: 1,
			steps: func(seq []string) []v1alpha1.StepStatus { return initStepStatuses(append(seq, healthCheckStep)) },
			reconcil: func(r *Reconciler, base, ps *v1alpha1.PromotionStep, seq []string) error {
				now := time.Now()
				_, err := r.handleStepError(context.Background(), zerolog.Nop(), base, ps, append(seq, healthCheckStep),
					map[int]steps.StepTiming{0: {Started: now.Add(-time.Second), Finished: now}}, retryable, nil)
				return err
			},
			want: []uint64{1, 0}},
		{name: "a step fails after the engine closed steps (transition)", state: StatePromoting, index: 1,
			steps: func(seq []string) []v1alpha1.StepStatus { return initStepStatuses(append(seq, healthCheckStep)) },
			reconcil: func(r *Reconciler, base, ps *v1alpha1.PromotionStep, seq []string) error {
				now := time.Now()
				_, err := r.handleStepError(context.Background(), zerolog.Nop(), base, ps, append(seq, healthCheckStep),
					map[int]steps.StepTiming{
						0: {Started: now.Add(-2 * time.Second), Finished: now.Add(-time.Second)},
						1: {Started: now.Add(-time.Second), Finished: now},
					}, errors.New("step git-clone: GitClient not configured"), nil)
				return err
			},
			want: []uint64{1, 1}},
	}
	for n, tt := range tests {
		for _, failure := range []string{"fails once", "not found"} {
			t.Run(tt.name+"/"+failure, func(t *testing.T) {
				seq := []string{fmt.Sprintf("test-patch-%d-%d-a", n, len(failure)), fmt.Sprintf("test-patch-%d-%d-b", n, len(failure))}
				before := make([]uint64, len(seq))
				for i := range seq {
					before[i], _ = stepDurationSamples(t, seq[i])
				}
				ps := &v1alpha1.PromotionStep{
					ObjectMeta: metav1.ObjectMeta{Name: "step", Namespace: "default"},
					Spec:       v1alpha1.PromotionStepSpec{PipelineName: "p", BundleName: "b1", Environment: "prod"},
					Status:     v1alpha1.PromotionStepStatus{State: tt.state, Steps: tt.steps(seq), CurrentStepIndex: tt.index},
				}
				patches := 0
				c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
					WithStatusSubresource(&v1alpha1.PromotionStep{}).WithObjects(ps).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
							patch client.Patch, opts ...client.SubResourcePatchOption) error {
							patches++
							switch {
							case failure == "not found":
								return apierrors.NewNotFound(schema.GroupResource{Group: "kardinal.io", Resource: "promotionsteps"}, obj.GetName())
							case patches == 1:
								return errors.New("etcdserver: request timed out")
							}
							return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
						},
					}).Build()
				r := &Reconciler{Client: c}

				// Each attempt is a reconcile: it reads the step as stored.
				for attempt := 1; attempt <= 2; attempt++ {
					var got v1alpha1.PromotionStep
					require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ps), &got))
					err := tt.reconcil(r, got.DeepCopy(), &got, seq)
					if failure == "not found" {
						require.NoError(t, err, "a deleted step is not an error")
						break
					}
					if attempt == 1 {
						require.Error(t, err, "the first patch fails")
					} else {
						require.NoError(t, err)
					}
				}

				want := tt.want
				if failure == "not found" {
					want = make([]uint64, len(seq))
				}
				for i := range seq {
					count, _ := stepDurationSamples(t, seq[i])
					assert.Equal(t, want[i], count-before[i], "samples of step %d", i)
				}
			})
		}
	}
}
