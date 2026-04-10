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

	"github.com/rs/zerolog"
)

// Engine executes a named sequence of steps, accumulating outputs between steps.
// It is not safe for concurrent use by multiple goroutines.
type Engine struct {
	steps []string
}

// NewEngine constructs an Engine with the given ordered step names.
func NewEngine(steps []string) *Engine {
	return &Engine{steps: steps}
}

// StepNames returns the step sequence.
func (e *Engine) StepNames() []string {
	return e.steps
}

// ExecuteFrom executes steps starting at startIndex (0-based).
// It returns the index of the next step to execute (or len(steps) if all completed),
// the result of the last executed step, and any fatal error.
//
// ExecuteFrom is idempotent: re-executing from the same index repeats that step.
// Callers must persist the returned nextIndex in PromotionStep.status.currentStepIndex
// before returning from the reconciler.
func (e *Engine) ExecuteFrom(ctx context.Context, state *StepState, startIndex int) (nextIndex int, result StepResult, err error) {
	log := zerolog.Ctx(ctx)

	for i := startIndex; i < len(e.steps); i++ {
		name := e.steps[i]
		step, lookupErr := Lookup(name)
		if lookupErr != nil {
			return i, StepResult{Status: StepFailed, Message: lookupErr.Error()}, fmt.Errorf("step %d lookup: %w", i, lookupErr)
		}

		log.Info().Str("step", name).Int("index", i).Msg("executing step")

		result, err = step.Execute(ctx, state)
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
		case StepSuccess:
			// Continue to next step.
		}
	}

	return len(e.steps), StepResult{Status: StepSuccess, Message: "all steps complete"}, nil
}
