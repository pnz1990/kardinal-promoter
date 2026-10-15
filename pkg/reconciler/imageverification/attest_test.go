// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
)

const (
	builderL3 = "https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v2.1.0"
	commitSHA = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
)

// keys are the signers of an attestation test: release signs the image and
// its build attestations, security signs VEX, other is no authority.
type keys struct {
	release, security, other *signtest.Key
}

func newKeys(t *testing.T) keys {
	t.Helper()
	var k keys
	for _, p := range []**signtest.Key{&k.release, &k.security, &k.other} {
		key, err := signtest.NewKey()
		require.NoError(t, err)
		*p = key
	}
	return k
}

func (k keys) secrets(t *testing.T) []*corev1.Secret {
	t.Helper()
	rel, err := k.release.PublicPEM()
	require.NoError(t, err)
	sec, err := k.security.PublicPEM()
	require.NoError(t, err)
	return []*corev1.Secret{keySecret("cosign", rel), keySecret("security", sec)}
}

// attestIV is an ImageVerification of one image with the policy's
// attestations, the release and security key authorities.
func attestIV(timeout string, attestations ...v1alpha1.AttestationPolicy) *v1alpha1.ImageVerification {
	obj := newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, timeout)
	obj.Spec.Policy.Authorities = append(obj.Spec.Policy.Authorities, v1alpha1.SignatureAuthority{Name: "security",
		Key: &v1alpha1.KeyAuthority{SecretRef: v1alpha1.SecretKeyName{Name: "security", Key: "cosign.pub"}}})
	obj.Spec.Policy.Attestations = attestations
	return obj
}

func provenance(builder string) map[string]any {
	return map[string]any{
		"buildDefinition": map[string]any{
			"buildType": "https://actions.github.io/buildtypes/workflow/v1",
			"resolvedDependencies": []any{map[string]any{
				"uri": "git+https://github.com/myorg/app@refs/heads/main", "digest": map[string]any{"gitCommit": commitSHA}}},
		},
		"runDetails": map[string]any{"builder": map[string]any{"id": builder}},
	}
}

func vsa(resource string, levels ...string) map[string]any {
	return map[string]any{"resourceUri": resource, "verificationResult": "PASSED", "verifiedLevels": levels,
		"verifier": map[string]any{"id": "https://verifier.example"}}
}

// trivyScan is a cosign vuln predicate holding a Trivy report.
func trivyScan(vulns ...[2]string) map[string]any {
	var vs []any
	for _, v := range vulns {
		vs = append(vs, map[string]any{"VulnerabilityID": v[0], "Severity": v[1]})
	}
	return map[string]any{
		"scanner": map[string]any{"uri": "pkg:github/aquasecurity/trivy@0.58.0", "version": "0.58.0",
			"result": map[string]any{"Results": []any{map[string]any{"Vulnerabilities": vs}}}},
		"metadata": map[string]any{"scanStartedOn": "2026-10-14T08:59:00Z", "scanFinishedOn": "2026-10-14T09:00:00Z"},
	}
}

// openVEX is an OpenVEX document with one statement per (vulnerability,
// status) for the image.
func openVEX(product string, statements ...[2]string) map[string]any {
	var st []any
	for _, s := range statements {
		entry := map[string]any{"vulnerability": map[string]any{"name": s[0]}, "status": s[1],
			"products": []any{map[string]any{"@id": product}}}
		if s[1] == "not_affected" {
			entry["justification"] = "vulnerable_code_not_in_execute_path"
		}
		st = append(st, entry)
	}
	return map[string]any{"@context": "https://openvex.dev/ns/v0.2.0", "@id": "https://example.com/vex/1",
		"author": "security", "timestamp": "2026-10-14T10:00:00Z", "version": 1, "statements": st}
}

func attest(t *testing.T, k *signtest.Key, predicateType string, predicate any) []byte {
	t.Helper()
	b, err := k.Attest(repo, digestA, predicateType, predicate)
	require.NoError(t, err)
	return b
}

func sign(t *testing.T, k *signtest.Key) []byte {
	t.Helper()
	b, err := k.Bundle(repo, digestA)
	require.NoError(t, err)
	return b
}

func resultOf(t *testing.T, got *v1alpha1.ImageVerification, name string) v1alpha1.AttestationResult {
	t.Helper()
	for _, r := range got.Status.Attestations {
		if r.Name == name {
			return r
		}
	}
	require.Failf(t, "no attestation result", "%s in %+v", name, got.Status.Attestations)
	return v1alpha1.AttestationResult{}
}

// TestAttestations_SLSALevels (docs/design/20-attestations.md §3.3): the
// level is the one the policy grants the provenance's builder, 1 for a
// builder it does not list; a VSA from an attestation authority that
// passed for this image raises it; one for another resource, or from no
// authority, does not; a level below minLevel holds the version until the
// timeout fails it.
func TestAttestations_SLSALevels(t *testing.T) {
	k := newKeys(t)
	slsa := &v1alpha1.SLSAPolicy{Builders: []v1alpha1.SLSABuilder{{ID: builderL3, Level: 3}}}
	for _, tc := range []struct {
		name    string
		minimum int
		bundles func() [][]byte
		level   int
		source  string
		phase   string
		reason  string
	}{
		{"a listed builder", 3, func() [][]byte {
			return [][]byte{attest(t, k.release, iv.PredicateSLSAProvenance, provenance(builderL3))}
		}, 3, "builder", v1alpha1.ImageVerificationVerified, ""},
		{"an unlisted builder is level 1, below minLevel 2", 2, func() [][]byte {
			return [][]byte{attest(t, k.release, iv.PredicateSLSAProvenance, provenance("https://ci.example/builder"))}
		}, 1, "builder", v1alpha1.ImageVerificationPending, iv.ReasonSLSALevelTooLow},
		{"a VSA raises it", 3, func() [][]byte {
			return [][]byte{attest(t, k.release, iv.PredicateSLSAProvenance, provenance("https://ci.example/builder")),
				attest(t, k.release, iv.PredicateSLSAVSA, vsa(repo+"@"+digestA, "SLSA_BUILD_LEVEL_3"))}
		}, 3, "vsa", v1alpha1.ImageVerificationVerified, ""},
		{"a VSA for another resource does not", 2, func() [][]byte {
			return [][]byte{attest(t, k.release, iv.PredicateSLSAProvenance, provenance("https://ci.example/builder")),
				attest(t, k.release, iv.PredicateSLSAVSA, vsa(repo+"@sha256:"+strings.Repeat("b", 64), "SLSA_BUILD_LEVEL_3"))}
		}, 1, "builder", v1alpha1.ImageVerificationPending, iv.ReasonSLSALevelTooLow},
		{"a VSA from no authority does not", 2, func() [][]byte {
			return [][]byte{attest(t, k.release, iv.PredicateSLSAProvenance, provenance("https://ci.example/builder")),
				attest(t, k.other, iv.PredicateSLSAVSA, vsa(repo+"@"+digestA, "SLSA_BUILD_LEVEL_3"))}
		}, 1, "builder", v1alpha1.ImageVerificationPending, iv.ReasonSLSALevelTooLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := *slsa
			s.MinLevel = tc.minimum
			obj := attestIV("5m", v1alpha1.AttestationPolicy{Name: "provenance", PredicateType: iv.PredicateSLSAProvenance,
				Authorities: []string{"release"}, Required: true, SLSA: &s})
			secrets := k.secrets(t)
			h := newHarness(t, nil, obj, secrets[0], secrets[1])
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: tc.bundles()}
			got, _ := h.reconcile()
			assert.Equal(t, tc.phase, got.Status.Phase, got.Status.Message)
			assert.Equal(t, tc.reason, got.Status.Reason)
			r := resultOf(t, got, "provenance")
			assert.True(t, r.Verified)
			require.NotNil(t, r.SLSA)
			assert.Equal(t, tc.level, r.SLSA.BuildLevel)
			assert.Equal(t, tc.source, r.SLSA.LevelSource)
			assert.Equal(t, commitSHA, r.SLSA.SourceCommit)
			assert.Equal(t, "git+https://github.com/myorg/app@refs/heads/main", r.SLSA.SourceURI)
			assert.JSONEq(t, mustJSON(t, provenanceFor(tc.bundles, k)), r.Predicate)
			if tc.phase == v1alpha1.ImageVerificationPending {
				h.now = h.now.Add(6 * time.Minute)
				got, _ = h.reconcile()
				assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
				assert.Equal(t, iv.ReasonSLSALevelTooLow, got.Status.Reason)
			}
		})
	}
}

// provenanceFor is the provenance predicate of the first bundle (the tests
// build it from provenance()).
func provenanceFor(bundles func() [][]byte, _ keys) map[string]any {
	var b struct {
		DSSE struct {
			Payload []byte `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	_ = json.Unmarshal(bundles()[0], &b)
	var st struct {
		Predicate map[string]any `json:"predicate"`
	}
	_ = json.Unmarshal(b.DSSE.Payload, &st)
	return st.Predicate
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// TestAttestations_VEX (§3.4): not_affected and fixed suppress a finding,
// affected and under_investigation keep it, an alias matches, VEX from a
// signer that is not a vex authority (the scanner's own) is ignored, and
// the counts and effective IDs say so.
func TestAttestations_VEX(t *testing.T) {
	k := newKeys(t)
	scan := trivyScan([2]string{"CVE-2026-0001", "CRITICAL"}, [2]string{"CVE-2026-0002", "CRITICAL"},
		[2]string{"CVE-2026-0003", "HIGH"}, [2]string{"CVE-2026-0004", "HIGH"}, [2]string{"GHSA-aaaa-bbbb-cccc", "MEDIUM"},
		[2]string{"CVE-2026-0006", "LOW"}, [2]string{"CVE-2026-0007", "weird"})
	product := repo + "@" + digestA
	vexDoc := openVEX(product, [2]string{"CVE-2026-0001", "not_affected"}, [2]string{"CVE-2026-0003", "fixed"},
		[2]string{"CVE-2026-0002", "affected"}, [2]string{"CVE-2026-0004", "under_investigation"})
	// An alias: the statement names the CVE, the scan the GHSA.
	alias := openVEX("pkg:oci/app@sha256%3A"+strings.Repeat("a", 64), [2]string{"CVE-2026-0005", "not_affected"})
	alias["statements"].([]any)[0].(map[string]any)["vulnerability"].(map[string]any)["aliases"] = []any{"GHSA-aaaa-bbbb-cccc"}
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"},
		Required: true, VEX: &v1alpha1.VEXPolicy{Authorities: []string{"security"}}}
	for _, tc := range []struct {
		name      string
		vexSigner *signtest.Key
		effective v1alpha1.SeverityCounts
		ids       []string
		stmts     int
	}{
		{"VEX from the security authority", k.security, v1alpha1.SeverityCounts{Critical: 1, High: 1, Low: 1, Unknown: 1},
			[]string{"CVE-2026-0002", "CVE-2026-0004"}, 5},
		{"VEX from the scanner's own signer is ignored", k.release, v1alpha1.SeverityCounts{Critical: 2, High: 2, Medium: 1, Low: 1, Unknown: 1},
			[]string{"CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003", "CVE-2026-0004"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secrets := k.secrets(t)
			h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{
				attest(t, k.release, iv.PredicateCosignVuln, scan),
				attest(t, tc.vexSigner, iv.PredicateOpenVEX, vexDoc),
				attest(t, tc.vexSigner, iv.PredicateOpenVEX, alias)}}
			got, _ := h.reconcile()
			assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
			r := resultOf(t, got, "vulns")
			require.NotNil(t, r.Vulns)
			assert.Equal(t, v1alpha1.SeverityCounts{Critical: 2, High: 2, Medium: 1, Low: 1, Unknown: 1}, r.Vulns.Before)
			assert.Equal(t, tc.effective, r.Vulns.Effective)
			assert.Equal(t, tc.ids, r.Vulns.EffectiveIDs)
			assert.Equal(t, tc.stmts, r.Vulns.VEXStatements)
			assert.Equal(t, "trivy", r.Vulns.Scanner)
			assert.Equal(t, "0.58.0", r.Vulns.ScannerVersion)
			assert.Equal(t, "2026-10-14T09:00:00Z", r.Vulns.ScannedAt)
		})
	}
}

// TestAttestations_Verification (§3.2): an attestation counts only for the
// image's digest, with the policy's predicate type, from one of its
// authorities; a required one missing waits and fails at the timeout, a
// one not required is recorded as not verified and holds nothing.
func TestAttestations_Verification(t *testing.T) {
	k := newKeys(t)
	other, err := k.release.Attest(repo, "sha256:"+strings.Repeat("b", 64), iv.PredicateSLSAProvenance, provenance(builderL3))
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		required bool
		bundles  [][]byte
		phase    string
		message  string
	}{
		{"the wrong digest", true, [][]byte{other}, v1alpha1.ImageVerificationPending, "no verified attestation"},
		{"another predicate type", true, [][]byte{attest(t, k.release, "https://example.com/other/v1", map[string]any{})},
			v1alpha1.ImageVerificationPending, "no verified attestation"},
		{"an untrusted signer", true, [][]byte{attest(t, k.other, iv.PredicateSLSAProvenance, provenance(builderL3))},
			v1alpha1.ImageVerificationPending, "no verified attestation"},
		{"none, not required", false, nil, v1alpha1.ImageVerificationVerified, "no verified attestation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secrets := k.secrets(t)
			h := newHarness(t, nil, attestIV("5m", v1alpha1.AttestationPolicy{Name: "provenance",
				PredicateType: iv.PredicateSLSAProvenance, Authorities: []string{"release"}, Required: tc.required}),
				secrets[0], secrets[1])
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: tc.bundles}
			got, _ := h.reconcile()
			assert.Equal(t, tc.phase, got.Status.Phase, got.Status.Message)
			r := resultOf(t, got, "provenance")
			assert.False(t, r.Verified)
			assert.Contains(t, r.Message, tc.message)
			if tc.required {
				assert.Equal(t, iv.ReasonAttestationNotVerified, got.Status.Reason)
				h.now = h.now.Add(6 * time.Minute)
				got, _ = h.reconcile()
				assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
				assert.Equal(t, iv.ReasonAttestationNotVerified, got.Status.Reason)
			}
		})
	}
}

// TestAttestations_ArrivesLaterAndIsLatched: a required attestation pushed
// after the first check verifies on a later one; the newest of several
// is used and counted; once Verified nothing is asked again.
func TestAttestations_ArrivesLaterAndIsLatched(t *testing.T) {
	k := newKeys(t)
	secrets := k.secrets(t)
	h := newHarness(t, nil, attestIV("", v1alpha1.AttestationPolicy{Name: "tests", PredicateType: "https://in-toto.io/attestation/test-result/v0.1",
		Authorities: []string{"release"}, Required: true}), secrets[0], secrets[1])
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}}
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{
		attest(t, k.release, "https://in-toto.io/attestation/test-result/v0.1", map[string]any{"result": "FAILED"}),
		attest(t, k.release, "https://in-toto.io/attestation/test-result/v0.1", map[string]any{"result": "PASSED"})}}
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	r := resultOf(t, got, "tests")
	assert.Equal(t, 2, r.Count)
	assert.JSONEq(t, `{"result":"PASSED"}`, r.Predicate, "the newest (the last referrer, without timestamps)")
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, r.Digest)
	calls := h.reg.calls
	again, _ := h.reconcile()
	assert.Equal(t, got.Status, again.Status, "idempotent")
	assert.Equal(t, calls, h.reg.calls)
}

// TestAttestations_PredicateBounds (§4): a predicate over 64 KiB keeps its
// summary and says it was omitted; over 256 KiB in total, the largest go.
func TestAttestations_PredicateBounds(t *testing.T) {
	k := newKeys(t)
	big := func(n int) map[string]any { return map[string]any{"blob": strings.Repeat("x", n)} }
	var policies []v1alpha1.AttestationPolicy
	var bundles [][]byte
	sizes := map[string]int{"huge": 70 << 10, "aa": 60 << 10, "bb": 60 << 10, "cc": 60 << 10, "dd": 60 << 10, "ee": 50 << 10}
	for name, n := range sizes {
		pt := "https://example.com/" + name + "/v1"
		policies = append(policies, v1alpha1.AttestationPolicy{Name: name, PredicateType: pt, Authorities: []string{"release"}})
		bundles = append(bundles, attest(t, k.release, pt, big(n)))
	}
	secrets := k.secrets(t)
	h := newHarness(t, nil, attestIV("", policies...), secrets[0], secrets[1])
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: bundles}
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	total, omitted := 0, map[string]string{}
	for _, r := range got.Status.Attestations {
		assert.True(t, r.Verified, r.Name)
		total += len(r.Predicate)
		if r.PredicateOmitted != "" {
			omitted[r.Name] = r.PredicateOmitted
		}
	}
	assert.Equal(t, "64 KiB", omitted["huge"])
	assert.LessOrEqual(t, total, 256<<10)
	assert.Equal(t, "", omitted["ee"], "the smallest is kept")
	assert.Len(t, omitted, 2, "the 70 KiB one, and one 60 KiB one to fit 256 KiB")
	assert.Contains(t, []string{"aa", "bb", "cc", "dd"}, firstOther(omitted))
}

func firstOther(m map[string]string) string {
	for k := range m {
		if k != "huge" {
			return k
		}
	}
	return ""
}
