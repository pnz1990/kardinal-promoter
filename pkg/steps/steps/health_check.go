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

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&healthCheckStep{})
}

// healthCheckStep is a stub that always returns Success.
// Real health adapter implementations are added in Stage 7 (item 014).
type healthCheckStep struct{}

func (s *healthCheckStep) Name() string { return "health-check" }

func (s *healthCheckStep) Execute(_ context.Context, _ *parentsteps.StepState) (parentsteps.StepResult, error) {
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: "health check passed (stub — real adapters in Stage 7)",
	}, nil
}
