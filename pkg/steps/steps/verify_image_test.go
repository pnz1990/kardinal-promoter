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

package steps_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestVerifyImageStep_Registered verifies the step is in the registry.
func TestVerifyImageStep_Registered(t *testing.T) {
	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)
	assert.Equal(t, "verify-image", step.Name())
}

// TestVerifyImageStep_NoImages returns Success with "no images to verify" when the
// bundle has no images. This matches the behaviour of kustomize-set-image for
// the same empty-images case.
func TestVerifyImageStep_NoImages(t *testing.T) {
	state := &parentsteps.StepState{
		Bundle:  v1alpha1.BundleSpec{Type: "image"}, // no Images
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "no images to verify", result.Message)
}

// TestVerifyImageStep_CosignNotInPath returns StepFailed with a message
// containing "cosign" when cosign is not in PATH. This tests the binary-missing
// guard (O2 in spec.md).
func TestVerifyImageStep_CosignNotInPath(t *testing.T) {
	// Override PATH to an empty temp dir so cosign cannot be found.
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", t.TempDir())

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/example/app", Tag: "v1.0.0"}},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.Error(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "cosign",
		"failure message must mention cosign when binary is missing")
}

// TestVerifyImageStep_StubCosignSuccess verifies that when cosign exits 0, the
// step returns StepSuccess containing the image reference in the message.
func TestVerifyImageStep_StubCosignSuccess(t *testing.T) {
	// Create a stub cosign script that exits 0.
	stubDir := t.TempDir()
	stubScript := filepath.Join(stubDir, "cosign")
	require.NoError(t, os.WriteFile(stubScript, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", stubDir+":"+origPath)

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/example/app", Tag: "v1.0.0"},
			},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Contains(t, result.Message, "ghcr.io/example/app",
		"success message must include the verified image reference")
}

// TestVerifyImageStep_StubCosignFailure verifies that when cosign exits non-zero,
// the step returns StepFailed and wraps the error.
func TestVerifyImageStep_StubCosignFailure(t *testing.T) {
	// Create a stub cosign script that exits 1 with output.
	stubDir := t.TempDir()
	stubScript := filepath.Join(stubDir, "cosign")
	require.NoError(t, os.WriteFile(stubScript,
		[]byte("#!/bin/sh\necho 'no signatures found'\nexit 1\n"), 0o755))

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", stubDir+":"+origPath)

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/example/app", Tag: "v1.0.0"},
			},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.Error(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "ghcr.io/example/app",
		"failure message must include the image that failed verification")
	assert.Contains(t, result.Message, "no signatures found",
		"failure message must include cosign's output")
}

// TestVerifyImageStep_WithIssuerAndIdentityRegexp verifies that when
// cosign.issuer and cosign.identityRegexp inputs are set, they are forwarded to
// the cosign binary as --certificate-oidc-issuer and --certificate-identity-regexp.
func TestVerifyImageStep_WithIssuerAndIdentityRegexp(t *testing.T) {
	// Create a stub that records its arguments to a temp file.
	stubDir := t.TempDir()
	argFile := filepath.Join(stubDir, "args.txt")
	stubScript := filepath.Join(stubDir, "cosign")
	// Write args to file, exit 0.
	require.NoError(t, os.WriteFile(stubScript,
		[]byte(`#!/bin/sh
printf '%s\n' "$@" > `+argFile+`
exit 0
`), 0o755))

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", stubDir+":"+origPath)

	const testIssuer = "https://token.actions.githubusercontent.com"
	const testIdentity = "https://github.com/myorg/.*/.github/"

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/myorg/app", Digest: "sha256:abc123"},
			},
		},
		Inputs: map[string]string{
			"cosign.issuer":         testIssuer,
			"cosign.identityRegexp": testIdentity,
		},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	// Verify cosign was called with the expected flags.
	argsRaw, readErr := os.ReadFile(argFile)
	require.NoError(t, readErr)
	argsStr := string(argsRaw)
	assert.Contains(t, argsStr, "--certificate-oidc-issuer",
		"cosign must be called with --certificate-oidc-issuer")
	assert.Contains(t, argsStr, testIssuer,
		"cosign must receive the configured issuer URL")
	assert.Contains(t, argsStr, "--certificate-identity-regexp",
		"cosign must be called with --certificate-identity-regexp")
	assert.Contains(t, argsStr, testIdentity,
		"cosign must receive the configured identity regexp")
}

// TestVerifyImageStep_DigestPreferredOverTag verifies that when both Tag and
// Digest are set, the digest-pinned reference is used for verification.
func TestVerifyImageStep_DigestPreferredOverTag(t *testing.T) {
	// Create a stub that records its arguments.
	stubDir := t.TempDir()
	argFile := filepath.Join(stubDir, "args.txt")
	stubScript := filepath.Join(stubDir, "cosign")
	require.NoError(t, os.WriteFile(stubScript,
		[]byte(`#!/bin/sh
printf '%s\n' "$@" > `+argFile+`
exit 0
`), 0o755))

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", stubDir+":"+origPath)

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/example/app", Tag: "v1.0.0", Digest: "sha256:deadbeef"},
			},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	argsRaw, readErr := os.ReadFile(argFile)
	require.NoError(t, readErr)
	argsStr := string(argsRaw)
	assert.Contains(t, argsStr, "@sha256:deadbeef",
		"digest-pinned reference must be used when Digest is set")
	assert.NotContains(t, argsStr, ":v1.0.0",
		"tag reference must not be used when Digest is set")
}

// TestVerifyImageStep_MultipleImages verifies that each image is individually
// verified. If the first succeeds and the second fails, StepFailed is returned.
func TestVerifyImageStep_MultipleImages_FirstFailsFast(t *testing.T) {
	// Create a stub that fails.
	stubDir := t.TempDir()
	stubScript := filepath.Join(stubDir, "cosign")
	require.NoError(t, os.WriteFile(stubScript,
		[]byte("#!/bin/sh\necho 'verification error'\nexit 1\n"), 0o755))

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", stubDir+":"+origPath)

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/example/app", Tag: "v1.0.0"},
				{Repository: "ghcr.io/example/sidecar", Tag: "v2.0.0"},
			},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.Error(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

// TestVerifyImageStep_Idempotent verifies that running the step twice on the
// same images produces the same successful result (O6 in spec.md).
func TestVerifyImageStep_Idempotent(t *testing.T) {
	stubDir := t.TempDir()
	stubScript := filepath.Join(stubDir, "cosign")
	require.NoError(t, os.WriteFile(stubScript, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", stubDir+":"+origPath)

	state := &parentsteps.StepState{
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/example/app", Tag: "v1.0.0"},
			},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("verify-image")
	require.NoError(t, err)

	result1, err1 := step.Execute(context.Background(), state)
	require.NoError(t, err1)
	assert.Equal(t, parentsteps.StepSuccess, result1.Status)

	result2, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err2)
	assert.Equal(t, parentsteps.StepSuccess, result2.Status)
	assert.Equal(t, result1.Status, result2.Status, "repeated execution must yield same status")
}
