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
	"os/exec"
	"strings"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&verifyImageStep{})
}

// verifyImageStep verifies container image signatures using cosign.
// It is idempotent: cosign verify is a read-only operation and produces the
// same result for the same image and signing policy.
//
// Configuration (read from state.Inputs):
//   - cosign.issuer: OIDC certificate issuer URL (optional)
//     e.g. "https://token.actions.githubusercontent.com"
//   - cosign.identityRegexp: certificate identity regexp (optional)
//     e.g. "https://github.com/myorg/.*/\.github/workflows/release\.yml@refs/heads/main"
//
// When both cosign.issuer and cosign.identityRegexp are absent, the step runs
// `cosign verify <image>` without keyless identity constraints — verifying that
// at least one valid signature exists.
type verifyImageStep struct{}

func (s *verifyImageStep) Name() string { return "verify-image" }

func (s *verifyImageStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to verify"}, nil
	}

	// Verify cosign is available in PATH before iterating images.
	cosignPath, err := exec.LookPath("cosign")
	if err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: "cosign binary not found in PATH: install cosign (https://docs.sigstore.dev/cosign/system_config/installation/)",
		}, fmt.Errorf("verify-image: %w", err)
	}

	issuer := ""
	identityRegexp := ""
	if state.Inputs != nil {
		issuer = state.Inputs["cosign.issuer"]
		identityRegexp = state.Inputs["cosign.identityRegexp"]
	}

	var verified []string
	for _, img := range state.Bundle.Images {
		if img.Repository == "" {
			continue
		}

		ref := img.Repository
		if img.Tag != "" {
			ref = img.Repository + ":" + img.Tag
		}
		if img.Digest != "" {
			// Prefer digest-pinned reference for signature verification.
			ref = img.Repository + "@" + img.Digest
		}

		args := buildCosignArgs(ref, issuer, identityRegexp)

		cmd := exec.CommandContext(ctx, cosignPath, args...) //nolint:gosec // args are validated
		out, execErr := cmd.CombinedOutput()
		if execErr != nil {
			msg := fmt.Sprintf("signature verification failed for %s: %s", ref, strings.TrimSpace(string(out)))
			return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: msg,
			}, fmt.Errorf("verify-image: %s: %w", ref, execErr)
		}
		verified = append(verified, ref)
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("signature verified for %d image(s): %s", len(verified), strings.Join(verified, ", ")),
	}, nil
}

// buildCosignArgs constructs the cosign verify argument list for a given image
// reference and optional keyless identity constraints.
func buildCosignArgs(imageRef, issuer, identityRegexp string) []string {
	args := []string{"verify"}
	if issuer != "" {
		args = append(args, "--certificate-oidc-issuer", issuer)
	}
	if identityRegexp != "" {
		args = append(args, "--certificate-identity-regexp", identityRegexp)
	}
	args = append(args, imageRef)
	return args
}
