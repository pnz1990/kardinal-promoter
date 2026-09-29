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
	"strconv"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&waitForMergeStep{})
}

// scmRetryInterval is how long wait-for-merge waits after an SCM API error.
const scmRetryInterval = 30 * time.Second

// waitForMergeStep polls the SCM provider to check if the PR has been merged.
// It returns StepPending until the PR is merged, and StepSuccess once merged.
// An SCM API error (a 5xx, a rate limit) is transient: the step stays Pending
// and is retried after scmRetryInterval.
// It is idempotent: re-running after a crash rechecks the PR status.
type waitForMergeStep struct{}

func (s *waitForMergeStep) Name() string { return "wait-for-merge" }

func (s *waitForMergeStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.SCM == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "SCM provider not configured"}, nil
	}

	if noChanges(state) {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no PR to wait for: " + noChangesMessage}, nil
	}

	prNumStr, ok := state.Outputs["prNumber"]
	if !ok || prNumStr == "" {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "prNumber not in step outputs — open-pr must run first"}, nil
	}

	prNum, err := strconv.Atoi(prNumStr)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("invalid prNumber %q: %v", prNumStr, err)}, nil
	}

	repo, err := scm.RepoFromURL(state.Pipeline.Git.URL)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("wait for merge: %v", err)}, nil
	}

	merged, open, err := state.SCM.GetPRStatus(ctx, repo, prNum)
	if err != nil {
		return parentsteps.StepResult{
			Status:       parentsteps.StepPending,
			Message:      fmt.Sprintf("PR #%d: get PR status failed, retrying in %s: %v", prNum, scmRetryInterval, err),
			RequeueAfter: scmRetryInterval,
		}, nil
	}

	if merged {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: fmt.Sprintf("PR #%d merged", prNum),
		}, nil
	}

	if !open {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("PR #%d was closed without merging", prNum),
		}, nil
	}

	// PR is still open — pending.
	return parentsteps.StepResult{
		Status:  parentsteps.StepPending,
		Message: fmt.Sprintf("PR #%d is open, waiting for merge", prNum),
	}, nil
}
