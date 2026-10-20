// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
)

// TestVerifyStatements_EarliestTimestamp (QA-3 on #2121): an attestation
// stamped again later (a second RFC 3161 timestamp over the same signature
// from the trusted TSA) is dated from its first verified time, not the
// latest: stamping an old attestation again does not make it fresh.
func TestVerifyStatements_EarliestTimestamp(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	const issuer = "https://token.actions.githubusercontent.com"
	const identity = "https://github.com/myorg/app/.github/workflows/release.yml@refs/heads/main"
	digest := "sha256:" + "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34"
	before := time.Now().Truncate(time.Second)
	b, err := signtest.KeylessAttestStamped(vs, identity, issuer, "r.example/app", digest, "https://example.com/custom/v1",
		map[string]any{"ok": true}, 2100*time.Millisecond)
	require.NoError(t, err)
	stampedAgain := time.Now()
	id, err := verify.NewShortCertificateIdentity(issuer, "", identity, "")
	require.NoError(t, err)
	found, reasons := verifyStatements(digest, [][]byte{b}, "https://example.com/custom/v1",
		[]authority{{name: "ci", trusted: vs, identity: &id}})
	require.Len(t, found, 1, "%v", reasons)
	at := found[0].at
	assert.False(t, at.Before(before), "the first stamp is not before the test began: %s", at)
	assert.True(t, at.Before(stampedAgain.Add(-2*time.Second)), "dated from the first stamp (%s), not the second (about %s)", at, stampedAgain)

	// With a 2s maxAge the attestation is stale once 2s pass from the first
	// stamp, though the second is newer.
	m := metav1.NewTime(at)
	res := v1alpha1.AttestationResult{Verified: true, VerifiedAt: &m, TimeSource: timeCertificate}
	setAge(&res, 2*time.Second, at.Add(2*time.Second+500*time.Millisecond))
	assert.True(t, res.Stale)
}

// TestStatementTimeOf: the time a signed statement gives itself.
func TestStatementTimeOf(t *testing.T) {
	when := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, predicateType, predicate string
		want                           time.Time
	}{
		{"SLSA provenance v1", PredicateSLSAProvenance, `{"runDetails":{"metadata":{"finishedOn":"2026-10-14T09:00:00Z"}}}`, when},
		{"SLSA provenance v0.2", "https://slsa.dev/provenance/v0.2", `{"metadata":{"buildFinishedOn":"2026-10-14T09:00:00Z"}}`, when},
		// Both fields, whatever the version: the earlier.
		{"v0.2 with a later v1 field", "https://slsa.dev/provenance/v0.2",
			`{"metadata":{"buildFinishedOn":"2026-10-14T09:00:00Z"},"runDetails":{"metadata":{"finishedOn":"2026-10-14T11:00:00Z"}}}`, when},
		{"v0.2 with an earlier v1 field", "https://slsa.dev/provenance/v0.2",
			`{"metadata":{"buildFinishedOn":"2026-10-14T11:00:00Z"},"runDetails":{"metadata":{"finishedOn":"2026-10-14T09:00:00Z"}}}`, when},
		{"a SLSA verification summary", PredicateSLSAVSA, `{"timeVerified":"2026-10-14T09:00:00Z","verificationResult":"PASSED"}`, when},
		{"a scan", PredicateCosignVuln, `{"scanner":{"result":{}},"metadata":{"scanFinishedOn":"2026-10-14T09:00:00Z"}}`, when},
		{"provenance without one", PredicateSLSAProvenance, `{"runDetails":{"builder":{"id":"x"}}}`, time.Time{}},
		{"an unparsable time", PredicateSLSAProvenance, `{"runDetails":{"metadata":{"finishedOn":"yesterday"}}}`, time.Time{}},
		{"another predicate's own field", "https://example.com/custom/v1", `{"metadata":{"buildFinishedOn":"2026-10-14T09:00:00Z"}}`, time.Time{}},
		{"not JSON", PredicateSLSAProvenance, `{`, time.Time{}},
	} {
		got, ok := statementTimeOf(tc.predicateType, []byte(tc.predicate))
		assert.Equal(t, !tc.want.IsZero(), ok, tc.name)
		assert.True(t, tc.want.Equal(got), "%s: %s", tc.name, got)
	}
}

// TestDating_ReStamped (QA-3 on #2121, the re-timestamping attack): an old
// signed statement pushed again with only a fresh log entry or timestamp
// (its original stamp dropped) is dated by the time the statement gives
// when it has one, so it stays old; a key-signed one without one cannot be
// dated, unless datedBy: logTime trusts the log; a keyless one counts from
// its verified time, which its certificate bounds.
func TestDating_ReStamped(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	old := now.Add(-3 * time.Hour)
	reStamped := now.Add(-time.Minute) // the fresh stamp that replaced the original
	custom := v1alpha1.AttestationPolicy{Name: "tests", PredicateType: "https://example.com/custom/v1", MaxAge: "1h"}
	provenance := v1alpha1.AttestationPolicy{Name: "provenance", PredicateType: PredicateSLSAProvenance, MaxAge: "1h"}
	logTime := custom
	logTime.DatedBy = datedByLogTime
	oldProvenance := []byte(`{"runDetails":{"metadata":{"finishedOn":"` + old.Format(time.RFC3339) + `"}}}`)
	for _, tc := range []struct {
		name   string
		p      v1alpha1.AttestationPolicy
		vs     verifiedStatement
		source string
		stale  bool
	}{
		{"provenance: its finishedOn, not the fresh stamp", provenance,
			verifiedStatement{payloadDigest: "sha256:p", at: reStamped, predicate: oldProvenance}, timeStatement, true},
		{"key-signed, no statement time: cannot be dated", custom,
			verifiedStatement{payloadDigest: "sha256:k", at: reStamped, predicate: []byte(`{}`)}, "", true},
		{"key-signed, datedBy logTime: the stamp, as documented", logTime,
			verifiedStatement{payloadDigest: "sha256:l", at: reStamped, predicate: []byte(`{}`)}, timeLog, false},
		{"keyless, no statement time: the certificate-bound time", custom,
			verifiedStatement{payloadDigest: "sha256:c", at: reStamped, shortCert: true, predicate: []byte(`{}`)}, timeCertificate, false},
		{"key-signed, no time at all", logTime,
			verifiedStatement{payloadDigest: "sha256:n", predicate: []byte(`{}`)}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, source := dating(tc.vs, tc.p)
			assert.Equal(t, tc.source, source)
			a := newAgePick(tc.p, nil, now)
			a.seen = sightings(nil, []verifiedStatement{tc.vs}, now)
			at, dated := a.staleAt(tc.vs, time.Hour)
			assert.Equal(t, tc.source != "", dated)
			assert.Equal(t, tc.stale, !now.Before(at), "staleAt %s", at)
		})
	}
}
