// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package promotionstep

// State constants for PromotionStep.status.state.
// These are the user-visible states emitted by the reconciler.
// The Graph controller uses readyWhen: '${step.status.state == "Verified"}'
// to advance the promotion DAG.
const (
	// PhaseVerified is the terminal success state.
	PhaseVerified = "Verified"
	// PhaseFailed is the terminal failure state.
	PhaseFailedConst = "Failed"
	// PhaseWaitingForMerge indicates the reconciler is waiting for a PR merge.
	PhaseWaitingForMergeConst = "WaitingForMerge"
	// PhaseHealthChecking indicates health verification is running.
	PhaseHealthCheckingConst = "HealthChecking"
)
