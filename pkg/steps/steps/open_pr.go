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

	if noChanges(state) {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no PR opened: " + noChangesMessage}, nil
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

	data := scm.PRBody{
		PipelineName:         state.PipelineName,
		Environment:          state.Environment.Name,
		BundleName:           state.BundleName,
		Bundle:               state.Bundle,
		GateResults:          state.GateResults,
		UpstreamEnvironments: buildPRBodyUpstreamEnvs(state.UpstreamEnvironments),
		Pipeline:             state.Pipeline,
	}
	title := fmt.Sprintf("[kardinal] Promote %s to %s", state.BundleName, state.Environment.Name)
	// A rollback PR says what it restores: the title names the restored
	// version (docs/rollback.md), and the note names the Bundle it replaces,
	// the Bundle it restores (RollbackOf, not the rollback Bundle itself) and
	// who asked for it.
	if state.Bundle.Provenance != nil && state.Bundle.Provenance.RollbackOf != "" {
		data.RollbackOf = state.Bundle.Provenance.RollbackOf
		data.RestoredVersion = scm.BundleVersion(state.Bundle)
		data.RollbackFrom = state.RollbackFrom
		if state.RollbackFromBundle != nil {
			data.RollbackFromVersion = scm.BundleVersion(*state.RollbackFromBundle)
		}
		data.RolledBackBy = state.RequestedBy
		restores := data.RestoredVersion
		if restores == "" {
			restores = data.RollbackOf
		}
		title = fmt.Sprintf("[kardinal] Rollback %s to %s (restores %s)", state.Environment.Name, state.BundleName, restores)
	}

	body, err := scm.RenderPRBody(data)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("render PR body: %v", err)},
			fmt.Errorf("render PR body: %w", err)
	}

	repo, err := scm.RepoFromURL(state.Pipeline.Git.URL)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}

	prURL, prNum, err := state.SCM.OpenPR(ctx, repo, title, body, branch, state.Git.Branch)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}

	// Apply standard kardinal labels to the PR. Every PR gets kardinal/promotion
	// (a rollback is a forward promotion). A rollback (Provenance.RollbackOf is set)
	// also gets kardinal/rollback so operators can find rollback PRs (#402,
	// docs/rollback.md, docs/pr-evidence.md).
	baseLabels := []string{"kardinal", "kardinal/promotion"}
	if state.Bundle.Provenance != nil && state.Bundle.Provenance.RollbackOf != "" {
		baseLabels = append(baseLabels, "kardinal/rollback")
	}
	message := fmt.Sprintf("PR #%d: %s", prNum, prURL)
	outputs := map[string]string{
		"prURL":    prURL,
		"prNumber": fmt.Sprintf("%d", prNum),
	}
	if labelsErr := state.SCM.AddLabelsToPR(ctx, repo, prNum, baseLabels); labelsErr != nil {
		// Non-fatal: the PR exists; report the missing labels. The output
		// keeps the error once the step moves on to wait for the merge, whose
		// message replaces this one (docs/pr-evidence.md).
		zerolog.Ctx(ctx).Warn().Err(labelsErr).Int("pr", prNum).Strs("labels", baseLabels).Msg("add PR labels failed")
		message += fmt.Sprintf(" (adding labels failed: %v)", labelsErr)
		outputs[parentsteps.OutputPRLabelsError] = labelsErr.Error()
	} else if state.Outputs[parentsteps.OutputPRLabelsError] != "" {
		// An earlier PR's labels failed; this one's did not.
		outputs[parentsteps.OutputPRLabelsError] = ""
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: message,
		Outputs: outputs,
	}, nil
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
