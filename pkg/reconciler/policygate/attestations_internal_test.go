// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// attestedIV is an ImageVerification of bundle with results for two
// images: provenance (levels 3 and 2), vulns (counts on each) and tests
// (the predicate on the first image only).
func attestedIV(name, bundle string, created time.Time, phase string) *kardinalv1alpha1.ImageVerification {
	iv := &kardinalv1alpha1.ImageVerification{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
		CreationTimestamp: metav1.NewTime(created),
		Labels:            map[string]string{labelBundle: bundle, labelPipeline: "app"}}}
	iv.Status.Phase = phase
	for i, img := range []string{"ghcr.io/acme/api@sha256:aa", "ghcr.io/acme/web@sha256:bb"} {
		level := 3 - i
		iv.Status.Attestations = append(iv.Status.Attestations,
			kardinalv1alpha1.AttestationResult{Name: "provenance", Image: img, PredicateType: "https://slsa.dev/provenance/v1",
				Verified: true, Signer: "release", Authority: "release",
				SLSA: &kardinalv1alpha1.SLSAResult{BuildLevel: level, LevelSource: "builder", BuilderID: "https://builder.example",
					SourceCommit: "abc123def", SourceURI: "git+https://github.com/acme/app"}},
			kardinalv1alpha1.AttestationResult{Name: "vulns", Image: img, PredicateType: "https://cosign.sigstore.dev/attestation/vuln/v1",
				Verified: true, Vulns: &kardinalv1alpha1.VulnsResult{Scanner: "trivy", ScannedAt: []string{"2026-10-14T09:00:00Z", "2026-10-14T08:00:00Z"}[i],
					Before:       kardinalv1alpha1.SeverityCounts{Critical: 2, High: 1},
					Effective:    kardinalv1alpha1.SeverityCounts{High: 1},
					EffectiveIDs: []string{"CVE-2026-0003"}, VEXStatements: 2}})
	}
	iv.Status.Attestations = append(iv.Status.Attestations,
		kardinalv1alpha1.AttestationResult{Name: "tests", Image: "ghcr.io/acme/api@sha256:aa", PredicateType: "https://in-toto.io/attestation/test-result/v0.1",
			Verified: true, Predicate: `{"result":"PASSED"}`},
		kardinalv1alpha1.AttestationResult{Name: "tests", Image: "ghcr.io/acme/web@sha256:bb", PredicateType: "https://in-toto.io/attestation/test-result/v0.1",
			Verified: false, Message: "no verified attestation"},
		kardinalv1alpha1.AttestationResult{Name: "sbom", Image: "ghcr.io/acme/api@sha256:aa", PredicateType: "https://spdx.dev/Document",
			Verified: true, PredicateOmitted: "64 KiB"})
	return iv
}

func attestationReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	bundle := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default",
		Labels: map[string]string{labelPipeline: "app"}}, Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
		Provenance: &kardinalv1alpha1.BundleProvenance{CommitSHA: "abc123def"}}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(append(objs, bundle)...).
		WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	return r
}

// TestAttestationContext (docs/design/20-attestations.md §6): the gate's
// attestations.* is the newest ImageVerification of its Bundle, each name
// folded over the images, and the documented expressions decide on it; a
// version without one, or an omitted predicate, fails closed.
func TestAttestationContext(t *testing.T) {
	old := attestedIV("app-v1-verify-old", "app-v1", time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC), "Failed")
	old.Status.Attestations = nil
	r := attestationReconciler(t, old, attestedIV("app-v1-verify-new", "app-v1", time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), "Verified"),
		attestedIV("other", "app-v2", time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC), "Failed"))
	gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "prod-gate", Namespace: "default",
		Labels: map[string]string{labelPipeline: "app", labelEnvironment: "prod", labelBundle: "app-v1"}}}

	for _, tc := range []struct {
		expr string
		want bool
		err  string
	}{
		{"attestations.verified", true, ""},
		{"attestations.provenance.verified && attestations.provenance.slsa.buildLevel >= 2", true, ""},
		{"attestations.provenance.slsa.buildLevel >= 3", false, ""}, // the lowest image's level
		{"attestations.provenance.slsa.sourceCommit == bundle.provenance.commitSHA", true, ""},
		{"attestations.vulns.effective.critical == 0 && attestations.vulns.effective.high <= 2", true, ""},
		{"attestations.vulns.before.critical == 4", true, ""},
		{"attestations.vulns.scannedAt == '2026-10-14T08:00:00Z'", true, ""}, // the oldest scan
		{"'CVE-2026-0003' in attestations.vulns.effectiveIDs && size(attestations.vulns.effectiveIDs) == 1", true, ""},
		{"attestations.tests.verified", false, ""}, // one image has none
		// Only the first image has the predicate: it is left out, not
		// taken from that image (it would pass a version whose second
		// image has no passing tests).
		{`json.unmarshal(attestations.tests.predicate).result == "PASSED"`, false, "no such key"},
		{`attestations.tests.images.all(i, has(i.predicate) && json.unmarshal(i.predicate).result == "PASSED")`, false, ""},
		{`attestations.tests.images.exists(i, has(i.predicate) && json.unmarshal(i.predicate).result == "PASSED")`, true, ""},
		{"attestations.provenance.signer == 'release' && size(attestations.provenance.images) == 2", true, ""},
		{`json.unmarshal(attestations.sbom.predicate).name != ""`, false, "no such key"},
		{"attestations.missing.verified", false, "no such key"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			gate.Spec.Expression = tc.expr
			celCtx, _, err := r.buildContext(t.Context(), gate, "app-v1")
			require.NoError(t, err)
			ok, reason, err := r.eval.evaluate(t.Context(), tc.expr, celCtx)
			if tc.err != "" {
				assert.False(t, ok, "fails closed")
				if err != nil {
					assert.Contains(t, err.Error(), tc.err)
				} else {
					assert.Contains(t, reason, tc.err)
				}
				return
			}
			require.NoError(t, err, reason)
			assert.Equal(t, tc.want, ok, reason)
		})
	}

	// A version with no ImageVerification: verified false, no names.
	none := attestationReconciler(t)
	gate.Spec.Expression = "attestations.provenance.slsa.buildLevel >= 1"
	celCtx, _, err := none.buildContext(t.Context(), gate, "app-v1")
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"verified": false}, celCtx["attestations"])
	ok, _, _ := none.eval.evaluate(t.Context(), gate.Spec.Expression, celCtx)
	assert.False(t, ok, "fails closed")
}

// TestImageVerificationRequests: a change of an ImageVerification's results
// re-evaluates its Bundle's gate instances that read attestations, not the
// others, and not on a retry that changed nothing a gate reads.
func TestImageVerificationRequests(t *testing.T) {
	mk := func(name, bundle, expr string) *kardinalv1alpha1.PolicyGate {
		return &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			Labels: map[string]string{labelBundle: bundle}}, Spec: kardinalv1alpha1.PolicyGateSpec{Expression: expr}}
	}
	r := attestationReconciler(t, mk("reads", "app-v1", "attestations.verified"), mk("other-bundle", "app-v2", "attestations.verified"),
		mk("not-reading", "app-v1", "true"))
	iv := attestedIV("app-v1-verify", "app-v1", time.Now(), "Pending")
	reqs := r.imageVerificationRequests(t.Context(), iv)
	require.Len(t, reqs, 1)
	assert.Equal(t, "reads", reqs[0].Name)

	retried := iv.DeepCopy()
	retried.Status.Attempts++
	retried.ResourceVersion = "2"
	assert.False(t, imageVerificationStatusChanged.Update(updateEvent(iv, retried)), "a retry changes nothing a gate reads")
	verified := iv.DeepCopy()
	verified.Status.Phase = "Verified"
	assert.True(t, imageVerificationStatusChanged.Update(updateEvent(iv, verified)))
}

func updateEvent(o, n client.Object) event.UpdateEvent {
	return event.UpdateEvent{ObjectOld: o, ObjectNew: n}
}

// TestAttestationContext_ImagesDisagree: for a version with several images,
// a field that identifies one image's attestation is set only when every
// image agrees on it. Built from two repositories, by two builders, signed
// by two authorities: sourceURI, builderID, signer and the scanner are left
// out, so a gate on them fails closed instead of checking the first image;
// images checks each one; the aggregates stay.
func TestAttestationContext_ImagesDisagree(t *testing.T) {
	iv := attestedIV("app-v1-verify", "app-v1", time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), "Verified")
	for i := range iv.Status.Attestations {
		a := &iv.Status.Attestations[i]
		if a.Image != "ghcr.io/acme/web@sha256:bb" {
			continue
		}
		if a.SLSA != nil {
			a.SLSA.SourceURI, a.SLSA.BuilderID = "git+https://github.com/attacker/fork", "https://other-builder.example"
			a.Signer, a.Authority = "dev", "dev"
		}
		if a.Vulns != nil {
			a.Vulns.Scanner = "grype"
		}
	}
	r := attestationReconciler(t, iv)
	gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "prod-gate", Namespace: "default",
		Labels: map[string]string{labelPipeline: "app", labelEnvironment: "prod", labelBundle: "app-v1"}}}
	for _, tc := range []struct {
		expr string
		want bool
		err  string
	}{
		{"attestations.provenance.slsa.sourceURI == 'git+https://github.com/acme/app'", false, "no such key"},
		{"attestations.provenance.slsa.builderID == 'https://builder.example'", false, "no such key"},
		{"attestations.provenance.signer == 'release'", false, "no such key"},
		{"attestations.vulns.scanner == 'trivy'", false, "no such key"},
		{"attestations.provenance.images.all(i, i.slsa.sourceURI == 'git+https://github.com/acme/app')", false, ""},
		{"attestations.provenance.images.all(i, i.signer in ['release', 'dev'])", true, ""},
		{"attestations.provenance.slsa.sourceCommit == 'abc123def' && attestations.provenance.slsa.buildLevel == 2", true, ""},
		{"attestations.vulns.effective.high == 2", true, ""},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			gate.Spec.Expression = tc.expr
			celCtx, _, err := r.buildContext(t.Context(), gate, "app-v1")
			require.NoError(t, err)
			ok, reason, err := r.eval.evaluate(t.Context(), tc.expr, celCtx)
			if tc.err != "" {
				assert.False(t, ok, "fails closed")
				if err != nil {
					assert.Contains(t, err.Error(), tc.err)
				} else {
					assert.Contains(t, reason, tc.err)
				}
				return
			}
			require.NoError(t, err, reason)
			assert.Equal(t, tc.want, ok, reason)
		})
	}
}
