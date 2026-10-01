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

package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// Engine executes a named sequence of steps, accumulating outputs between steps.
// It is not safe for concurrent use by multiple goroutines.
type Engine struct {
	steps   []string
	timings map[int]StepTiming
}

// StepTiming is when a step executed by ExecuteFrom started and returned.
type StepTiming struct {
	Started  time.Time
	Finished time.Time
}

// NewEngine constructs an Engine with the given ordered step names.
func NewEngine(steps []string) *Engine {
	return &Engine{steps: steps}
}

// StepNames returns the step sequence.
func (e *Engine) StepNames() []string {
	return e.steps
}

// Timings returns, by step index, when each step the last ExecuteFrom call
// executed started and returned. A step run twice (after a StepRestart) has
// the times of its last run. The caller records step durations from these, so
// a step that finishes within one reconcile has its real duration
// (kardinal_step_duration_seconds, status.steps[].durationMs).
func (e *Engine) Timings() map[int]StepTiming {
	return e.timings
}

// MaxSequenceRestarts bounds how many times one ExecuteFrom call restarts the
// sequence from the first step after a step returned StepRestart.
const MaxSequenceRestarts = 3

// ExecuteFrom executes steps starting at startIndex (0-based).
// It returns the index of the next step to execute (or len(steps) if all completed),
// the result of the last executed step, and any fatal error.
//
// ExecuteFrom is idempotent: re-executing from the same index repeats that step.
// Callers must persist the returned nextIndex in PromotionStep.status.currentStepIndex
// before returning from the reconciler.
//
// A step that returns StepRestart (for example git-push after the base branch
// moved) makes the engine run the sequence again from index 0, at most
// MaxSequenceRestarts times; after that the step is reported as Failed.
// ExecuteFrom never returns StepRestart.
//
// When state.StepTimeoutSeconds > 0, each step is executed with a per-step
// context.WithTimeout to prevent hung steps from blocking the reconciler indefinitely.
func (e *Engine) ExecuteFrom(ctx context.Context, state *StepState, startIndex int) (nextIndex int, result StepResult, err error) {
	log := zerolog.Ctx(ctx)
	restarts := 0
	e.timings = make(map[int]StepTiming)

	for i := startIndex; i < len(e.steps); i++ {
		name := e.steps[i]
		step, lookupErr := Lookup(name)
		if lookupErr != nil {
			return i, StepResult{Status: StepFailed, Message: lookupErr.Error()}, fmt.Errorf("step %d lookup: %w", i, lookupErr)
		}

		log.Info().Str("step", name).Int("index", i).Msg("executing step")

		started := time.Now()
		result, err = executeStep(ctx, step, state)
		e.timings[i] = StepTiming{Started: started, Finished: time.Now()}
		if err != nil {
			return i, result, fmt.Errorf("step %s: %w", name, err)
		}

		// Merge step outputs into accumulated state.
		if state.Outputs == nil {
			state.Outputs = make(map[string]string)
		}
		for k, v := range result.Outputs {
			state.Outputs[k] = v
		}

		switch result.Status {
		case StepPending:
			// Step is still in progress — return current index so the reconciler can requeue.
			return i, result, nil
		case StepFailed:
			return i, result, fmt.Errorf("step %s: %s", name, result.Message)
		case StepRestart:
			if restarts >= MaxSequenceRestarts {
				result.Status = StepFailed
				result.Message = fmt.Sprintf("%s (gave up after %d restarts)", result.Message, restarts)
				return i, result, fmt.Errorf("step %s: %s", name, result.Message)
			}
			restarts++
			log.Info().Str("step", name).Int("restart", restarts).Str("reason", result.Message).
				Msg("restarting step sequence from the first step")
			i = -1 // the loop increment makes this 0
		case StepSuccess:
			// Continue to next step.
		default:
			return i, result, fmt.Errorf("step %s: unknown status %q", name, result.Status)
		}
	}

	return len(e.steps), StepResult{Status: StepSuccess, Message: "all steps complete"}, nil
}

// executeStep runs one step, applying the per-step timeout when configured.
// The timeout context is released as soon as the step returns.
func executeStep(ctx context.Context, step Step, state *StepState) (StepResult, error) {
	if state.StepTimeoutSeconds <= 0 {
		return step.Execute(ctx, state)
	}
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(state.StepTimeoutSeconds)*time.Second)
	defer cancel()
	return step.Execute(stepCtx, state)
}
