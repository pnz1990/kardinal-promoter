//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// scanFinishedAt is a Trivy scan with no findings that finished at t.
func scanFinishedAt(t time.Time) map[string]any {
	return map[string]any{
		"scanner": map[string]any{"uri": "pkg:github/aquasecurity/trivy@0.58.0", "version": "0.58.0",
			"result": map[string]any{"Results": []any{}}},
		"metadata": map[string]any{"scanStartedOn": t.Add(-time.Minute).UTC().Format(time.RFC3339),
			"scanFinishedOn": t.UTC().Format(time.RFC3339)},
	}
}

// TestStep_ImageVerificationAttestationAge (#2044): with maxAge 5m, a scan
// that finished 10 minutes ago is stale as soon as it verifies: the
// ImageVerification is Verified, test promotes, and prod's gate on
// !attestations.vulns.stale holds the version. Once a fresh scan is
// attached, the reconciler finds it at its next re-fetch (at most 5 minutes
// later), the result is no longer stale, and prod promotes.
//
// Covers IMGV-ATTEST-AGE-01.
func TestStep_ImageVerificationAttestationAge(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	sigs := newSignatures(t, a.ns)
	release, err := signtest.NewKey()
	require.NoError(t, err)
	pem, err := release.PublicPEM()
	require.NoError(t, err)
	policy := keyPolicy(t, e, a.ns, sigs, pem, "3m")
	policy.Attestations = []v1alpha1.AttestationPolicy{{Name: "vulns", PredicateType: iv.PredicateCosignVuln,
		Authorities: []string{"release"}, MaxAge: "5m"}}
	p := a.pipeline(nil)
	p.Spec.ImageVerification = policy
	a.apply(t, p)
	const gate = "prod-fresh-scan"
	e.CreateGate(t, framework.Gate(a.ns, gate, "prod", "!attestations.vulns.stale && attestations.vulns.effective.critical == 0", ""))
	waitPipelineValid(t, e, a.ns, pipelineName)
	push := func(predicate map[string]any) {
		b, err := release.Attest(fixtures.Image, fixtures.V2Digest, iv.PredicateCosignVuln, predicate)
		require.NoError(t, err)
		sigs.pushBundle(t, fixtures.V2Digest, b)
	}
	sig, err := release.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	sigs.pushBundle(t, fixtures.V2Digest, sig)
	push(scanFinishedAt(time.Now().Add(-10 * time.Minute)))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	got := waitImageVerification(t, e, a.ns, bundle, "Verified")
	require.Len(t, got.Status.Attestations, 1)
	assert.True(t, got.Status.Attestations[0].Stale, "a 10-minute-old scan with a 5m maxAge")
	require.NotNil(t, got.Status.Attestations[0].StaleAt)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", gate, false, "", gateTimeout)

	push(scanFinishedAt(time.Now()))
	framework.Eventually(t, 8*time.Minute, "the fresh scan replaces the stale one", func(ctx context.Context) (bool, string) {
		v, err := imageVerification(ctx, e, a.ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		if v.Status.Phase != v1alpha1.ImageVerificationVerified {
			t.Fatalf("ImageVerification left Verified: %s %s", v.Status.Phase, v.Status.Message)
		}
		r := v.Status.Attestations[0]
		return !r.Stale, fmt.Sprintf("stale=%v staleAt=%v message=%q", r.Stale, r.StaleAt, r.Message)
	})
	e.WaitGateReady(t, a.ns, bundle, "prod", gate, true, "", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}
