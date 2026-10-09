// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func ovAt(by string, created *time.Time, expires time.Time) kardinalv1alpha1.PolicyGateOverride {
	o := kardinalv1alpha1.PolicyGateOverride{Reason: "r", CreatedBy: by, ExpiresAt: metav1.NewTime(expires)}
	if created != nil {
		c := metav1.NewTime(*created)
		o.CreatedAt = &c
	}
	return o
}

// TestFindActiveOverride_FirstSeenAndCap: an override ends at the earlier of
// its expiresAt and firstSeen + the cap, and one whose createdAt is more than
// the clock skew after firstSeen never counts.
func TestFindActiveOverride_FirstSeenAndCap(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	const cap = 24 * time.Hour
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	tests := []struct {
		name      string
		o         kardinalv1alpha1.PolicyGateOverride
		firstSeen time.Duration // relative to now; 0 = not recorded yet
		active    bool
		end       time.Time
	}{
		{"created now", ovAt("a", at(0), now.Add(time.Hour)), 0, true, now.Add(time.Hour)},
		{"no createdAt (older overrides)", ovAt("a", nil, now.Add(time.Hour)), 0, true, now.Add(time.Hour)},
		{"within the clock skew", ovAt("a", at(4*time.Minute), now.Add(time.Hour)), 0, true, now.Add(time.Hour)},
		{"expiresAt past the cap ends at firstSeen + cap", ovAt("a", at(-time.Hour), now.Add(30*24*time.Hour)), -time.Hour, true, now.Add(23 * time.Hour)},
		{"seen a cap ago: over", ovAt("a", at(-25*time.Hour), now.Add(30*24*time.Hour)), -25 * time.Hour, false, time.Time{}},
		{"created in the future", ovAt("a", at(30*24*time.Hour), now.Add(31*24*time.Hour)), 0, false, time.Time{}},
		{"expired", ovAt("a", at(-2*time.Hour), now.Add(-time.Hour)), -2 * time.Hour, false, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := map[string]time.Time{}
			if tt.firstSeen != 0 {
				first[overrideKey(&tt.o)] = now.Add(tt.firstSeen)
			}
			got, end := findActiveOverride([]kardinalv1alpha1.PolicyGateOverride{tt.o}, "prod", now, first, cap)
			assert.Equal(t, tt.active, got != nil)
			if tt.active {
				assert.Equal(t, tt.end, end)
			}
		})
	}
}

// TestFindActiveOverride_ThirtyChainedEntries: 30 entries dated T, T+cap,
// T+2cap... written at once each pass the admission policy (expiresAt -
// createdAt <= cap), but all are first seen at T: only the first counts, and
// only for one cap. Without firstSeen they would hold the gate open for 30
// days.
func TestFindActiveOverride_ThirtyChainedEntries(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	const cap = 24 * time.Hour
	var chain []kardinalv1alpha1.PolicyGateOverride
	for i := 0; i < 30; i++ {
		created := t0.Add(time.Duration(i) * cap)
		chain = append(chain, ovAt(fmt.Sprintf("mallory-%d", i), &created, created.Add(cap)))
	}
	first := firstSeenOf(observeOverrides(chain, nil, t0))
	assert.Len(t, first, 30)
	for _, tc := range []struct {
		after  time.Duration
		active bool
	}{
		{0, true}, {23 * time.Hour, true}, {cap, false}, {cap + time.Minute, false},
		{5 * cap, false}, {29*cap + time.Hour, false},
	} {
		got, _ := findActiveOverride(chain, "prod", t0.Add(tc.after), first, cap)
		assert.Equal(t, tc.active, got != nil, "after %s", tc.after)
		if got != nil {
			assert.Equal(t, "mallory-0", got.CreatedBy)
		}
	}
	// Later reconciles keep the recorded firstSeen.
	again := observeOverrides(chain, observeOverrides(chain, nil, t0), t0.Add(10*cap))
	for _, o := range again {
		assert.Equal(t, t0, o.FirstSeen.Time)
	}
}
