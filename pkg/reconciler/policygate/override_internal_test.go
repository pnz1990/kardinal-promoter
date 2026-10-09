// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestFindActiveOverride_CreatedAt: an override dated in the future (beyond
// the clock skew) does not count yet, so a self-declared createdAt cannot
// stretch the admission policy's bound on expiresAt - createdAt.
func TestFindActiveOverride_CreatedAt(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ov := func(created *time.Time, expires time.Time) kardinalv1alpha1.PolicyGateOverride {
		o := kardinalv1alpha1.PolicyGateOverride{Reason: "r", ExpiresAt: metav1.NewTime(expires)}
		if created != nil {
			c := metav1.NewTime(*created)
			o.CreatedAt = &c
		}
		return o
	}
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	tests := []struct {
		name   string
		o      kardinalv1alpha1.PolicyGateOverride
		active bool
	}{
		{"created now", ov(at(0), now.Add(time.Hour)), true},
		{"no createdAt (older overrides)", ov(nil, now.Add(time.Hour)), true},
		{"created within the clock skew", ov(at(4*time.Minute), now.Add(time.Hour)), true},
		{"created in the future", ov(at(30*24*time.Hour), now.Add(31*24*time.Hour)), false},
		{"expired", ov(at(-2*time.Hour), now.Add(-time.Hour)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findActiveOverride([]kardinalv1alpha1.PolicyGateOverride{tt.o}, "prod", now)
			assert.Equal(t, tt.active, got != nil)
		})
	}
}
