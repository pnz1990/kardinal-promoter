// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func TestAgeBaseAndSetAge(t *testing.T) {
	signed := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	at := func(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }
	scan := func(s string) *v1alpha1.VulnsResult { return &v1alpha1.VulnsResult{ScannedAt: s} }
	cases := []struct {
		name string
		res  v1alpha1.AttestationResult
		want time.Time
	}{
		{"the signing time", v1alpha1.AttestationResult{VerifiedAt: at(signed)}, signed},
		{"a scan's time", v1alpha1.AttestationResult{Vulns: scan("2026-10-14T08:00:00Z")}, signed.Add(-time.Hour)},
		{"the scan's earlier time", v1alpha1.AttestationResult{VerifiedAt: at(signed), Vulns: scan("2026-10-14T08:00:00Z")}, signed.Add(-time.Hour)},
		// A scan cannot be newer than its signature: a later scannedAt does
		// not keep it fresh longer.
		{"a scannedAt after the signature", v1alpha1.AttestationResult{VerifiedAt: at(signed), Vulns: scan("2026-10-20T00:00:00Z")}, signed},
		{"an unparsable scannedAt", v1alpha1.AttestationResult{VerifiedAt: at(signed), Vulns: scan("yesterday")}, signed},
		{"no time", v1alpha1.AttestationResult{}, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, tc.want.Equal(ageBase(tc.res)), "%v != %v", tc.want, ageBase(tc.res))
		})
	}

	res := v1alpha1.AttestationResult{Verified: true, VerifiedAt: at(signed)}
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
