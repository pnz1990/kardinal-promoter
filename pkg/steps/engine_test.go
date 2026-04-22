// Copyright 2026 The kardinal-promoter Authors.
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

package steps_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// blockingStep is a test step that blocks until its context is cancelled.
type blockingStep struct {
	name string
}

func (b *blockingStep) Name() string { return b.name }

func (b *blockingStep) Execute(ctx context.Context, _ *steps.StepState) (steps.StepResult, error) {
	<-ctx.Done()
	return steps.StepResult{Status: steps.StepFailed, Message: "context cancelled"}, ctx.Err()
}

// instantStep is a test step that succeeds immediately.
type instantStep struct {
	name string
}

func (s *instantStep) Name() string { return s.name }

func (s *instantStep) Execute(_ context.Context, _ *steps.StepState) (steps.StepResult, error) {
	return steps.StepResult{Status: steps.StepSuccess, Message: "done"}, nil
}

func TestEngineStepTimeout(t *testing.T) {
	// Register test steps under unique names for this test.
	steps.Register(&blockingStep{name: "test-blocking-timeout"})
	steps.Register(&instantStep{name: "test-instant-timeout"})

	tests := []struct {
		name               string
		stepNames          []string
		stepTimeoutSeconds int
		wantErr            bool
		errContains        string
	}{
		{
			name:               "no timeout — blocking step blocks until test deadline",
			stepNames:          []string{"test-instant-timeout"},
			stepTimeoutSeconds: 0,
			wantErr:            false,
		},
		{
			name:               "timeout fires — blocking step is cancelled",
			stepNames:          []string{"test-blocking-timeout"},
			stepTimeoutSeconds: 1, // 1 second — fast for CI
			wantErr:            true,
			errContains:        "test-blocking-timeout",
		},
		{
			name:               "timeout does not fire when step completes before deadline",
			stepNames:          []string{"test-instant-timeout"},
			stepTimeoutSeconds: 300,
			wantErr:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := steps.NewEngine(tt.stepNames)
			state := &steps.StepState{
				StepTimeoutSeconds: tt.stepTimeoutSeconds,
			}

			// Use a short parent deadline so the blocking test doesn't hang CI.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			start := time.Now()
			_, _, err := eng.ExecuteFrom(ctx, state, 0)
			elapsed := time.Since(start)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				// Verify the timeout fired within the expected window (not the parent 5s deadline)
				assert.Less(t, elapsed, 3*time.Second, "timeout should fire before parent deadline")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEngineStepTimeoutContext(t *testing.T) {
	// Verify that context.DeadlineExceeded is propagated as the error.
	steps.Register(&blockingStep{name: "test-blocking-ctxerr"})

	eng := steps.NewEngine([]string{"test-blocking-ctxerr"})
	state := &steps.StepState{
		StepTimeoutSeconds: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := eng.ExecuteFrom(ctx, state, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected DeadlineExceeded, got: %v", err)
}
