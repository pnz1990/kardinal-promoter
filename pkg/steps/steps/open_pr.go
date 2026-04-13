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

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&openPRStep{})
}

// openPRStep opens a pull request via the SCM provider.
// It is idempotent: if a prNumber is already in outputs, it skips the creation.
type openPRStep struct{}

func (s *openPRStep) Name() string { return "open-pr" }

func (s *openPRStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.SCM == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "SCM provider not configured"}, nil
	}

	// Idempotency: skip if PR already opened.
	if prURL, ok := state.Outputs["prURL"]; ok && prURL != "" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: "PR already open: " + prURL,
			Outputs: map[string]string{"prURL": prURL},
		}, nil
	}

	branch, ok := state.Outputs["branch"]
	if !ok || branch == "" {
		branch = fmt.Sprintf("kardinal/%s/%s", state.BundleName, state.Environment.Name)
	}

	title := fmt.Sprintf("[kardinal] Promote %s to %s", state.BundleName, state.Environment.Name)

	body, err := scm.RenderPRBody(scm.PRBody{
		PipelineName:         state.PipelineName,
		Environment:          state.Environment.Name,
		BundleName:           state.BundleName,
		Bundle:               state.Bundle,
		GateResults:          state.GateResults,
		UpstreamEnvironments: buildPRBodyUpstreamEnvs(state.UpstreamEnvironments),
		Pipeline:             state.Pipeline,
		RepoURL:              state.Pipeline.Git.URL,
	})
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("render PR body: %v", err)},
			fmt.Errorf("open-pr render body: %w", err)
	}

	// Repo is derived from Pipeline.Git.URL: strip protocol and .git suffix.
	repo := extractRepo(state.Pipeline.Git.URL)

	prURL, prNum, err := state.SCM.OpenPR(ctx, repo, title, body, branch, state.Git.Branch)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("open PR: %v", err)},
			fmt.Errorf("open-pr: %w", err)
	}

	// Apply standard kardinal labels to the PR.
	// Include kardinal/rollback when this bundle is a rollback (Provenance.RollbackOf is set).
	// This is required by issue #402 / docs/rollback.md — rollback PRs must have the
	// kardinal/rollback label so operators can filter them from promotion PRs.
	baseLabels := []string{"kardinal", "kardinal/promotion"}
	if state.Bundle.Provenance != nil && state.Bundle.Provenance.RollbackOf != "" {
		baseLabels = append(baseLabels, "kardinal/rollback")
	}
	if labelsErr := state.SCM.AddLabelsToPR(ctx, repo, prNum, baseLabels); labelsErr != nil {
		// Non-fatal: log but do not fail the step.
		_ = labelsErr
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("PR #%d: %s", prNum, prURL),
		Outputs: map[string]string{
			"prURL":    prURL,
			"prNumber": fmt.Sprintf("%d", prNum),
		},
	}, nil
}

// extractRepo extracts "owner/repo" from a GitHub HTTPS URL.
// e.g., "https://github.com/owner/repo" → "owner/repo"
// e.g., "https://github.com/owner/repo.git" → "owner/repo"
func extractRepo(url string) string {
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			repo := url[len(prefix):]
			// Strip .git suffix.
			if len(repo) > 4 && repo[len(repo)-4:] == ".git" {
				repo = repo[:len(repo)-4]
			}
			return repo
		}
	}
	return url
}

// buildPRBodyUpstreamEnvs converts []v1alpha1.EnvironmentStatus to []scm.PRBodyUpstreamEnv,
// pre-computing the elapsed time for each environment at call time rather than at
// template render time. This eliminates SCM-4: time.Since() inside PR template execution.
func buildPRBodyUpstreamEnvs(envs []v1alpha1.EnvironmentStatus) []scm.PRBodyUpstreamEnv {
	now := time.Now().UTC()
	result := make([]scm.PRBodyUpstreamEnv, 0, len(envs))
	for _, env := range envs {
		e := scm.PRBodyUpstreamEnv{
			Name:            env.Name,
			Phase:           env.Phase,
			HealthCheckedAt: env.HealthCheckedAt,
		}
		if env.HealthCheckedAt != nil && !env.HealthCheckedAt.IsZero() {
			e.Elapsed = scm.FormatElapsed(env.HealthCheckedAt.Time, now)
		}
		result = append(result, e)
	}
	return result
}
