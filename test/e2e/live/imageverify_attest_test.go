//go:build e2e

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

package live

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

const (
	// attestBuilderL3 is the builder the policy grants level 3.
	attestBuilderL3 = "https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v2.1.0"
	// attestCommit is the source commit of the provenance and the version.
	attestCommit = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	// attestGate is prod's supply-chain gate.
	attestGate = "prod-supply-chain"
)

// attestedApp is an app whose Pipeline requires signed images with
// provenance and a vulnerability scan (VEX from the security key), and
// whose prod gate wants level-3 provenance from the version's commit and no
// effective critical finding. The release key signed the image; the
// attestations are pushed by the caller.
type attestedApp struct {
	*app
	sigs              signatures
	release, security *signtest.Key
}

func newAttestedApp(t *testing.T, e *framework.Env) *attestedApp {
	t.Helper()
	ctx := context.Background()
	a := &attestedApp{app: newArgoApp(t, e, "test", "prod")}
	a.sigs = newSignatures(t, a.ns)
	var err error
	a.release, err = signtest.NewKey()
	require.NoError(t, err)
	a.security, err = signtest.NewKey()
	require.NoError(t, err)
	relPEM, err := a.release.PublicPEM()
	require.NoError(t, err)
	secPEM, err := a.security.PublicPEM()
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "security", Namespace: a.ns,
		Labels: map[string]string{"kardinal.io/referenceable": "true"}}, Data: map[string][]byte{"cosign.pub": secPEM}}))
	policy := keyPolicy(t, e, a.ns, a.sigs, relPEM, "3m")
	policy.Authorities = append(policy.Authorities, v1alpha1.SignatureAuthority{Name: "security",
		Key: &v1alpha1.KeyAuthority{SecretRef: v1alpha1.SecretKeyName{Name: "security", Key: "cosign.pub"}}})
	policy.Attestations = []v1alpha1.AttestationPolicy{
		{Name: "provenance", PredicateType: iv.PredicateSLSAProvenance, Authorities: []string{"release"}, Required: true,
			SLSA: &v1alpha1.SLSAPolicy{Builders: []v1alpha1.SLSABuilder{{ID: attestBuilderL3, Level: 3}}}},
		{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, Required: true,
			VEX: &v1alpha1.VEXPolicy{Authorities: []string{"security"}}},
	}
	p := a.pipeline(nil)
	p.Spec.ImageVerification = policy
	a.apply(t, p)
	g := framework.Gate(a.ns, attestGate, "prod", "attestations.provenance.slsa.buildLevel >= 3 && "+
		"attestations.provenance.slsa.sourceCommit == bundle.provenance.commitSHA && attestations.vulns.effective.critical == 0", "")
	e.CreateGate(t, g)
	waitPipelineValid(t, e, a.ns, pipelineName)

	sig, err := a.release.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	a.sigs.pushBundle(t, fixtures.V2Digest, sig)
	return a
}

// attest pushes an attestation of the image signed with k.
func (a *attestedApp) attest(t *testing.T, k *signtest.Key, predicateType string, predicate any) {
	t.Helper()
	b, err := k.Attest(fixtures.Image, fixtures.V2Digest, predicateType, predicate)
	require.NoError(t, err)
	a.sigs.pushBundle(t, fixtures.V2Digest, b)
}

func attestProvenance(builder string) map[string]any {
	return map[string]any{
		"buildDefinition": map[string]any{"buildType": "https://actions.github.io/buildtypes/workflow/v1",
			"resolvedDependencies": []any{map[string]any{"uri": "git+https://github.com/myorg/podinfo@refs/heads/main",
				"digest": map[string]any{"gitCommit": attestCommit}}}},
		"runDetails": map[string]any{"builder": map[string]any{"id": builder}},
	}
}

func attestScan() map[string]any {
	return map[string]any{
		"scanner": map[string]any{"uri": "pkg:github/aquasecurity/trivy@0.58.0", "version": "0.58.0",
			"result": map[string]any{"Results": []any{map[string]any{"Vulnerabilities": []any{
				map[string]any{"VulnerabilityID": "CVE-2026-0001", "Severity": "CRITICAL"},
				map[string]any{"VulnerabilityID": "CVE-2026-0002", "Severity": "HIGH"}}}}}},
		"metadata": map[string]any{"scanStartedOn": "2026-10-14T08:59:00Z", "scanFinishedOn": "2026-10-14T09:00:00Z"},
	}
}

func attestVEX() map[string]any {
	return map[string]any{"@context": "https://openvex.dev/ns/v0.2.0", "@id": "https://example.com/vex/podinfo",
		"author": "security", "timestamp": "2026-10-14T10:00:00Z", "version": 1,
		"statements": []any{map[string]any{"vulnerability": map[string]any{"name": "CVE-2026-0001"}, "status": "not_affected",
			"justification": "vulnerable_code_not_in_execute_path",
			"products":      []any{map[string]any{"@id": fixtures.Image + "@" + fixtures.V2Digest}}}}}
}

// TestStep_ImageVerificationAttestationsPassGate (docs/image-verification.md,
// Attestations): the image is signed, carries level-3 provenance from the
// version's commit and a Trivy scan whose critical finding the security
// key's OpenVEX says does not apply; the ImageVerification records the level,
// the source and the counts before and after VEX, and prod's gate over
// attestations.* lets the version in.
//
// Covers IMGV-ATTEST-01, IMGV-ATTEST-VEX-01.
func TestStep_ImageVerificationAttestationsPassGate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newAttestedApp(t, e)
	a.attest(t, a.release, iv.PredicateSLSAProvenance, attestProvenance(attestBuilderL3))
	a.attest(t, a.release, iv.PredicateCosignVuln, attestScan())
	a.attest(t, a.security, iv.PredicateOpenVEX, attestVEX())
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest, "--commit", attestCommit)

	got := waitImageVerification(t, e, a.ns, bundle, "Verified")
	results := map[string]v1alpha1.AttestationResult{}
	for _, r := range got.Status.Attestations {
		results[r.Name] = r
	}
	require.Contains(t, results, "provenance")
	require.NotNil(t, results["provenance"].SLSA)
	assert.Equal(t, 3, results["provenance"].SLSA.BuildLevel)
	assert.Equal(t, attestCommit, results["provenance"].SLSA.SourceCommit)
	require.NotNil(t, results["vulns"].Vulns)
	assert.Equal(t, v1alpha1.SeverityCounts{Critical: 1, High: 1}, results["vulns"].Vulns.Before)
	assert.Equal(t, v1alpha1.SeverityCounts{High: 1}, results["vulns"].Vulns.Effective, "the critical finding is not_affected")
	assert.Equal(t, 1, results["vulns"].Vulns.VEXStatements)

	e.WaitGateReady(t, a.ns, bundle, "prod", attestGate, true, "", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestStep_ImageVerificationAttestationsHoldGate: the same image with
// provenance from a builder the policy does not list (level 1) and a scan
// with no VEX: the version is verified and promotes to test, but prod's gate
// holds it, its reason the expression's result, until nothing changes.
//
// Covers IMGV-ATTEST-01, IMGV-ATTEST-VEX-01.
func TestStep_ImageVerificationAttestationsHoldGate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newAttestedApp(t, e)
	a.attest(t, a.release, iv.PredicateSLSAProvenance, attestProvenance("https://ci.example.com/self-hosted-builder"))
	a.attest(t, a.release, iv.PredicateCosignVuln, attestScan())
	// VEX signed by the release key, which is not a vex authority: ignored.
	a.attest(t, a.release, iv.PredicateOpenVEX, attestVEX())
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest, "--commit", attestCommit)

	got := waitImageVerification(t, e, a.ns, bundle, "Verified")
	for _, r := range got.Status.Attestations {
		switch r.Name {
		case "provenance":
			require.NotNil(t, r.SLSA)
			assert.Equal(t, 1, r.SLSA.BuildLevel, "a builder the policy does not list")
		case "vulns":
			require.NotNil(t, r.Vulns)
			assert.Equal(t, 1, r.Vulns.Effective.Critical, "VEX from the scanner's own key is ignored")
			assert.Zero(t, r.Vulns.VEXStatements)
		}
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	g := e.WaitGateReady(t, a.ns, bundle, "prod", attestGate, false, "", gateTimeout)
	assert.True(t, strings.Contains(g.Status.Reason, "false") || strings.Contains(g.Status.Reason, "attestations"), g.Status.Reason)
	time.Sleep(10 * time.Second)
	step, ok, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	if ok {
		assert.NotEqual(t, "Verified", step.Status.State, "prod is held by the gate")
	}
}
