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

// healthCheckStep marks the end of the step sequence. It checks nothing: the
// PromotionStep reconciler checks health in its HealthChecking state after the
// sequence finishes, using the adapters in pkg/health.
type healthCheckStep struct{}

func (s *healthCheckStep) Name() string { return "health-check" }

func (s *healthCheckStep) Execute(_ context.Context, _ *parentsteps.StepState) (parentsteps.StepResult, error) {
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: "steps done; health is checked next by the PromotionStep reconciler",
	}, nil
}
