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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestHandleStepError_Classification proves the retry rule of
// C03-promotionstep-06 for each shape of engine error: a step error is
// retried, a step error marked steps.Permanent (a configuration the step
// refuses) fails at once, and so does a step that reported StepFailed without
// an error.
func TestHandleStepError_Classification(t *testing.T) {
	refused := errors.New("update.strategy argocd cannot honour approval: pr-review")
	tests := []struct {
		name        string
		err         error
		retryCount  int
		wantState   string
		wantRetry   int
		wantRequeue time.Duration
		wantMsg     string
		wantNoMsg   string
	}{
		{name: "step error is retried",
			err:       fmt.Errorf("step git-push: %w", fmt.Errorf("git push origin main: %w", context.DeadlineExceeded)),
			wantState: StatePromoting, wantRetry: 1, wantRequeue: 10 * time.Second, wantMsg: "retrying in 10s (1/5)"},
		{name: "step error fails once the retries are used up", retryCount: maxStepRetries,
			err:       fmt.Errorf("step git-push: %w", context.DeadlineExceeded),
			wantState: StateFailed, wantMsg: "gave up after 5 retries"},
		{name: "permanent step error fails at once",
			err:       fmt.Errorf("step argocd-set-image: %w", steps.Permanent(refused)),
			wantState: StateFailed, wantMsg: refused.Error(), wantNoMsg: "retr"},
		{name: "StepFailed without an error fails at once",
			err:       errors.New("step git-clone: GitClient not configured"),
			wantState: StateFailed, wantMsg: "GitClient not configured", wantNoMsg: "retr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: "step", Namespace: "default"},
				Spec:       v1alpha1.PromotionStepSpec{PipelineName: "p", BundleName: "b1", Environment: "prod"},
				Status:     v1alpha1.PromotionStepStatus{State: StatePromoting, RetryCount: tt.retryCount},
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}).WithObjects(ps).Build()
			r := &Reconciler{Client: c}
			base := ps.DeepCopy()

			res, err := r.handleStepError(context.Background(), zerolog.Nop(), base, ps,
				[]string{"git-clone", "argocd-set-image", "git-push"}, nil, tt.err, nil, gitCredential{})
			require.NoError(t, err)

			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ps), &got))
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.wantRetry, got.Status.RetryCount)
			assert.Equal(t, tt.wantRequeue, res.RequeueAfter)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			if tt.wantNoMsg != "" {
				assert.NotContains(t, got.Status.Message, tt.wantNoMsg)
			}
		})
	}
}
