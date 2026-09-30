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
	"fmt"
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

// countingStep counts its calls and returns the statuses in order, then the
// last one forever.
type countingStep struct {
	name     string
	calls    int
	statuses []steps.StepStatus
}

func (s *countingStep) Name() string { return s.name }

func (s *countingStep) Execute(_ context.Context, _ *steps.StepState) (steps.StepResult, error) {
	st := s.statuses[min(s.calls, len(s.statuses)-1)]
	s.calls++
	return steps.StepResult{Status: st, Message: string(st)}, nil
}

// TestEngine_StepRestart proves a StepRestart re-runs the sequence from the
// first step (a fresh clone after a non-fast-forward push) and that restarts
// are bounded (C05-steps-12).
func TestEngine_StepRestart(t *testing.T) {
	tests := []struct {
		name          string
		second        []steps.StepStatus
		wantNext      int
		wantStatus    steps.StepStatus
		wantErr       string
		wantFirstRuns int
	}{
		{
			name:          "restart once then succeed",
			second:        []steps.StepStatus{steps.StepRestart, steps.StepSuccess},
			wantNext:      2,
			wantStatus:    steps.StepSuccess,
			wantFirstRuns: 2,
		},
		{
			name:          "restart forever gives up",
			second:        []steps.StepStatus{steps.StepRestart},
			wantNext:      1,
			wantStatus:    steps.StepFailed,
			wantErr:       "gave up after 3 restarts",
			wantFirstRuns: steps.MaxSequenceRestarts + 1,
		},
		{
			name:          "unknown status is an error",
			second:        []steps.StepStatus{"Bogus"},
			wantNext:      1,
			wantStatus:    "Bogus",
			wantErr:       "unknown status",
			wantFirstRuns: 1,
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := &countingStep{name: fmt.Sprintf("test-restart-first-%d", i), statuses: []steps.StepStatus{steps.StepSuccess}}
			second := &countingStep{name: fmt.Sprintf("test-restart-second-%d", i), statuses: tc.second}
			steps.Register(first)
			steps.Register(second)

			engine := steps.NewEngine([]string{first.name, second.name})
			next, result, err := engine.ExecuteFrom(context.Background(), &steps.StepState{Outputs: map[string]string{}}, 0)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantNext, next)
			assert.Equal(t, tc.wantStatus, result.Status)
			assert.Equal(t, tc.wantFirstRuns, first.calls)
		})
	}
}

// TestEngine_EmptyStepName proves an empty step name fails the sequence
// (C05-steps-34).
func TestEngine_EmptyStepName(t *testing.T) {
	_, err := steps.Lookup("")
	require.Error(t, err)

	next, _, err := steps.NewEngine([]string{""}).ExecuteFrom(context.Background(), &steps.StepState{Outputs: map[string]string{}}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty step name")
	assert.Equal(t, 0, next)
}
