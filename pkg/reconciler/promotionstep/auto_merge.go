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

package promotionstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// autoMergeMaxAttempts bounds the attempts to enable auto-merge while the
// SCM keeps answering with a retryable error (still checking the PR, 5xx).
const autoMergeMaxAttempts = 8

// autoMergeRetry is the wait before attempt n+1 to enable auto-merge.
func autoMergeRetry(n int) time.Duration {
	d := 5 * time.Second << min(n, 5)
	return min(d, 2*time.Minute)
}

// syncAutoMerge keeps the SCM's auto-merge of a waiting step's PR in line
// with whether the promotion may merge now (pr.merge.auto): on while the
// Pipeline is not paused and every required gate is ready, off otherwise. A
// freeze (ChangeWindow) closes the gates that reference it, so it turns
// auto-merge off too. The state is in status.outputs (prAutoMerge and the
// keys next to it), so a restart resumes it; every SCM call is followed by
// a status write. It returns whether the outputs changed and how soon to
// look again (0: the usual wait-for-merge poll).
func (r *Reconciler) syncAutoMerge(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep, repo string, prNumber int) (bool, time.Duration) {
	out := ps.Status.Outputs
	state := out[steps.OutputPRAutoMerge]
	if state == "" || state == steps.AutoMergeFailed {
		return false, 0
	}
	// The step's own provider (spec.scmProvider, #1517), as for the PR
	// itself; one that cannot be used is retried at the next poll.
	provider, err := r.scmFor(ctx, ps)
	if err != nil {
		log.Warn().Err(err).Msg("the step's SCM provider cannot be used for auto-merge; checking again later")
		return false, requeueWaitForMerge
	}
	ctrl, ok := provider.(scm.PRController)
	if !ok {
		return false, 0
	}
	why, err := r.autoMergeBlocked(ctx, ps)
	if err != nil {
		log.Warn().Err(err).Msg("cannot check whether auto-merge may stay on; checking again later")
		return false, requeueWaitForMerge
	}

	if why != "" {
		if state == steps.AutoMergeSuspended && out[steps.OutputPRAutoMergeError] == why {
			return false, 0
		}
		// pending does not mean off: an enable can succeed at the SCM while
		// the client sees a timeout or a 5xx, or the controller can stop
		// between the enable and the status write. Turning it off is a no-op
		// on every provider when it is not on (#1683).
		if state == steps.AutoMergeEnabled || state == steps.AutoMergePending {
			if err := ctrl.DisableAutoMerge(ctx, repo, prNumber); err != nil && !errors.Is(err, scm.ErrPRControlUnsupported) {
				log.Warn().Err(err).Int("pr", prNumber).Msg("disable auto-merge failed; retrying")
				out[steps.OutputPRAutoMergeError] = fmt.Sprintf("%s, and turning auto-merge off failed: %v", why, err)
				return true, requeueWaitForMerge
			}
			log.Info().Int("pr", prNumber).Str("reason", why).Msg("auto-merge turned off")
		}
		out[steps.OutputPRAutoMerge] = steps.AutoMergeSuspended
		out[steps.OutputPRAutoMergeError] = why
		delete(out, steps.OutputPRAutoMergeRetryAt)
		return true, 0
	}

	if state == steps.AutoMergeEnabled {
		// A reconcile from a stale cache may have left the keys of an
		// earlier attempt next to enabled.
		changed := false
		for _, k := range []string{steps.OutputPRAutoMergeError, steps.OutputPRAutoMergeRetryAt, steps.OutputPRAutoMergeAttempts} {
			if _, ok := out[k]; ok {
				delete(out, k)
				changed = true
			}
		}
		return changed, 0
	}
	now := r.now()
	if at, err := time.Parse(time.RFC3339, out[steps.OutputPRAutoMergeRetryAt]); err == nil && now.Before(at) {
		return false, at.Sub(now)
	}
	var opts scm.MergeOptions
	if err := json.Unmarshal([]byte(out[steps.OutputPRMergeOptions]), &opts); err != nil {
		out[steps.OutputPRAutoMerge] = steps.AutoMergeFailed
		out[steps.OutputPRAutoMergeError] = "the merge options in status.outputs.prMergeOptions cannot be read"
		return true, 0
	}
	err = ctrl.EnableAutoMerge(ctx, repo, prNumber, opts)
	if err == nil {
		log.Info().Int("pr", prNumber).Msg("auto-merge turned on")
		out[steps.OutputPRAutoMerge] = steps.AutoMergeEnabled
		delete(out, steps.OutputPRAutoMergeError)
		delete(out, steps.OutputPRAutoMergeRetryAt)
		delete(out, steps.OutputPRAutoMergeAttempts)
		return true, 0
	}
	attempts, _ := strconv.Atoi(out[steps.OutputPRAutoMergeAttempts])
	attempts++
	out[steps.OutputPRAutoMergeError] = err.Error()
	if !scm.IsRetryableMergeError(err) || attempts >= autoMergeMaxAttempts {
		log.Warn().Err(err).Int("pr", prNumber).Msg("auto-merge not turned on; the PR waits for a merge by hand")
		out[steps.OutputPRAutoMerge] = steps.AutoMergeFailed
		delete(out, steps.OutputPRAutoMergeRetryAt)
		return true, 0
	}
	wait := autoMergeRetry(attempts - 1)
	out[steps.OutputPRAutoMergeAttempts] = strconv.Itoa(attempts)
	out[steps.OutputPRAutoMergeRetryAt] = now.Add(wait).UTC().Format(time.RFC3339)
	out[steps.OutputPRAutoMerge] = steps.AutoMergePending
	return true, wait
}

// autoMergeBlocked says why the step's PR may not merge now: the Pipeline
// is paused, or a required gate is missing or not ready. "" means it may.
func (r *Reconciler) autoMergeBlocked(ctx context.Context, ps *v1alpha1.PromotionStep) (string, error) {
	paused, err := lifecycle.IsPaused(ctx, r.Client, ps.Namespace, ps.Spec.PipelineName)
	if err != nil {
		return "", fmt.Errorf("check pause of pipeline %s: %w", ps.Spec.PipelineName, err)
	}
	if paused {
		return fmt.Sprintf("pipeline %s is paused", ps.Spec.PipelineName), nil
	}
	for _, name := range ps.Spec.RequiredGates {
		var gate v1alpha1.PolicyGate
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ps.Namespace}, &gate); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Sprintf("gate %s is gone", name), nil
			}
			return "", fmt.Errorf("get policy gate %s: %w", name, err)
		}
		if !gate.Status.Ready {
			return fmt.Sprintf("gate %s is closed", name), nil
		}
	}
	return "", nil
}

// autoMergeNote is the part of a WaitingForMerge message that says what
// auto-merge is doing; "" when the environment does not use it.
func autoMergeNote(outputs map[string]string) string {
	why := outputs[steps.OutputPRAutoMergeError]
	switch outputs[steps.OutputPRAutoMerge] {
	case steps.AutoMergeEnabled:
		return "auto-merge enabled"
	case steps.AutoMergePending:
		if why != "" {
			return "auto-merge pending: " + why
		}
		return "auto-merge pending"
	case steps.AutoMergeSuspended:
		return "auto-merge off: " + why
	case steps.AutoMergeFailed:
		return "auto-merge failed: " + why
	}
	return ""
}

var errNoPR = errors.New("no PR number in status.outputs")

// stepPR returns the repository and number of the step's PR.
func stepPR(pipeline *v1alpha1.Pipeline, ps *v1alpha1.PromotionStep) (string, int, error) {
	n, err := strconv.Atoi(ps.Status.Outputs["prNumber"])
	if err != nil || n <= 0 {
		return "", 0, errNoPR
	}
	repo, err := scm.RepoFromURL(pipeline.Spec.Git.URL)
	if err != nil {
		return "", 0, err
	}
	return repo, n, nil
}
