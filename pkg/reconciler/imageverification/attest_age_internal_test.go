// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func TestAgeBaseAndSetAge(t *testing.T) {
	signed := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	at := func(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }
	stmt := func(s string) *metav1.Time {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return at(t)
	}
	cases := []struct {
		name string
		res  v1alpha1.AttestationResult
		want time.Time
	}{
		{"a keyless signing time", v1alpha1.AttestationResult{VerifiedAt: at(signed), TimeSource: timeCertificate}, signed},
		{"a log time, opted in", v1alpha1.AttestationResult{VerifiedAt: at(signed), TimeSource: timeLog}, signed},
		{"a statement's time", v1alpha1.AttestationResult{StatementTime: stmt("2026-10-14T08:00:00Z"), TimeSource: timeStatement}, signed.Add(-time.Hour)},
		{"the statement's earlier time", v1alpha1.AttestationResult{VerifiedAt: at(signed), StatementTime: stmt("2026-10-14T08:00:00Z"), TimeSource: timeStatement}, signed.Add(-time.Hour)},
		// A statement cannot be newer than its signature: a later time
		// does not keep it fresh longer.
		{"a statement time after the signature", v1alpha1.AttestationResult{VerifiedAt: at(signed), StatementTime: stmt("2026-10-14T09:30:00Z"), TimeSource: timeStatement}, signed},
		{"no time source", v1alpha1.AttestationResult{VerifiedAt: at(signed)}, time.Time{}},
		{"a statement source with no time", v1alpha1.AttestationResult{TimeSource: timeStatement}, time.Time{}},
		{"a log source with no time", v1alpha1.AttestationResult{TimeSource: timeLog}, time.Time{}},
		{"no time", v1alpha1.AttestationResult{}, time.Time{}},
		// A signed time more than futureSkew ahead of now cannot be used:
		// the attestation cannot be dated (QA-3 on #2121).
		{"a future statement time", v1alpha1.AttestationResult{DatedAt: at(signed), StatementTime: stmt("2099-01-01T00:00:00Z"), TimeSource: timeStatement}, time.Time{}},
		{"a future statement time, with a signing time", v1alpha1.AttestationResult{VerifiedAt: at(signed), StatementTime: stmt("2099-01-01T00:00:00Z"), TimeSource: timeStatement}, time.Time{}},
		{"a future signing time", v1alpha1.AttestationResult{VerifiedAt: at(signed.Add(2 * time.Hour)), TimeSource: timeCertificate}, time.Time{}},
		{"a statement time within the skew", v1alpha1.AttestationResult{StatementTime: stmt("2026-10-14T10:04:00Z"), TimeSource: timeStatement}, signed.Add(time.Hour + 4*time.Minute)},
		// Never later than when kardinal first recorded it.
		{"a statement older than its first sighting", v1alpha1.AttestationResult{DatedAt: at(signed), StatementTime: stmt("2026-10-14T08:00:00Z"), TimeSource: timeStatement}, signed.Add(-time.Hour)},
		{"first seen alone does not date it", v1alpha1.AttestationResult{DatedAt: at(signed)}, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := signed.Add(time.Hour)
			assert.True(t, tc.want.Equal(ageBase(tc.res, now)), "%v != %v", tc.want, ageBase(tc.res, now))
		})
	}

	res := v1alpha1.AttestationResult{Verified: true, VerifiedAt: at(signed), TimeSource: timeCertificate}
	setAge(&res, 2*time.Hour, signed.Add(time.Hour))
	assert.Equal(t, signed.Add(2*time.Hour), res.StaleAt.UTC())
	assert.False(t, res.Stale)
	setAge(&res, 2*time.Hour, signed.Add(2*time.Hour))
	assert.True(t, res.Stale, "stale at staleAt")
	setAge(&res, 0, signed)
	assert.Nil(t, res.StaleAt, "no maxAge, no age")
	unverified := v1alpha1.AttestationResult{VerifiedAt: at(signed)}
	setAge(&unverified, time.Hour, signed.Add(2*time.Hour))
	assert.False(t, unverified.Stale, "an attestation that did not verify is not stale, it is missing")
}

func TestDateAttestation(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	m := metav1.NewTime(t0)
	prev := v1alpha1.AttestationResult{Verified: true, Digest: "sha256:aa", DatedAt: &m}
	same := v1alpha1.AttestationResult{Verified: true, Digest: "sha256:aa"}
	dateAttestation(&same, &prev, t0.Add(time.Hour))
	assert.Equal(t, t0, same.DatedAt.Time, "the same attestation keeps its date")
	other := v1alpha1.AttestationResult{Verified: true, Digest: "sha256:bb"}
	dateAttestation(&other, &prev, t0.Add(time.Hour))
	assert.Equal(t, t0.Add(time.Hour), other.DatedAt.Time, "another attestation is dated now")
	seenBefore := v1alpha1.AttestationResult{Verified: true, Digest: "sha256:cc",
		FirstSeen: []v1alpha1.AttestationSighting{{Digest: "sha256:cc", At: metav1.NewTime(t0.Add(-time.Hour))}}}
	dateAttestation(&seenBefore, &prev, t0.Add(time.Hour))
	assert.Equal(t, t0.Add(-time.Hour), seenBefore.DatedAt.Time, "an attestation seen before keeps its first sighting")
	prev.FirstSeen = []v1alpha1.AttestationSighting{{Digest: "sha256:dd", At: metav1.NewTime(t0.Add(-2 * time.Hour))}}
	fromPrev := v1alpha1.AttestationResult{Verified: true, Digest: "sha256:dd"}
	dateAttestation(&fromPrev, &prev, t0.Add(time.Hour))
	assert.Equal(t, t0.Add(-2*time.Hour), fromPrev.DatedAt.Time, "a sighting in the previous result counts")
	missing := v1alpha1.AttestationResult{Digest: "sha256:aa", DatedAt: &m}
	dateAttestation(&missing, &prev, t0.Add(time.Hour))
	assert.Nil(t, missing.DatedAt, "an attestation that did not verify has no date")
}

func TestRefetchInterval(t *testing.T) {
	assert.Equal(t, minRefetch, refetchInterval(time.Minute))
	assert.Equal(t, 30*time.Minute, refetchInterval(2*time.Hour))
	assert.Equal(t, maxRefetch, refetchInterval(7*24*time.Hour))
}

func TestPolicyMaxAge(t *testing.T) {
	assert.Equal(t, 24*time.Hour, policyMaxAge(v1alpha1.AttestationPolicy{MaxAge: "24h"}))
	assert.Zero(t, policyMaxAge(v1alpha1.AttestationPolicy{}))
	assert.Zero(t, policyMaxAge(v1alpha1.AttestationPolicy{MaxAge: "0s"}))
}

// TestSightings: every verified digest is recorded once with when it was
// first seen; an existing sighting keeps its time; beyond maxSightings the
// oldest are dropped.
func TestSightings(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	vs := func(d string) verifiedStatement { return verifiedStatement{payloadDigest: d} }
	got := sightings(nil, []verifiedStatement{vs("sha256:a"), vs("sha256:b"), vs("sha256:a"), vs("")}, t0)
	assert.Equal(t, []v1alpha1.AttestationSighting{{Digest: "sha256:a", At: metav1.NewTime(t0)}, {Digest: "sha256:b", At: metav1.NewTime(t0)}}, got)
	got = sightings(got, []verifiedStatement{vs("sha256:b"), vs("sha256:c")}, t0.Add(time.Hour))
	assert.Len(t, got, 3)
	at, ok := sightingOf(got, "sha256:b")
	assert.True(t, ok)
	assert.Equal(t, t0, at.Time, "seen again: keeps its first sighting")

	var many []verifiedStatement
	for i := range maxSightings + 10 {
		many = append(many, vs(fmt.Sprintf("sha256:%03d", i)))
	}
	assert.Equal(t, 16, maxSightings, "the cap (QA-3 on #2121), also the CRD's MaxItems")
	capped := sightings(got, many, t0.Add(2*time.Hour))
	assert.Len(t, capped, maxSightings, "capped")
	_, ok = sightingOf(capped, "sha256:a")
	assert.False(t, ok, "the oldest sightings are dropped first")
	_, ok = sightingOf(capped, fmt.Sprintf("sha256:%03d", maxSightings+9))
	assert.True(t, ok, "the newest are kept")
}
