// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestParseInterval: an interval below the documented 30s minimum is raised to
// 30s, not replaced by the 5m default; empty, zero and unparsable values use
// the default.
func TestParseInterval(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "", want: 5 * time.Minute},
		{raw: "0", want: 5 * time.Minute},
		{raw: "bogus", want: 5 * time.Minute},
		{raw: "1ms", want: 30 * time.Second},
		{raw: "10s", want: 30 * time.Second},
		{raw: "29s", want: 30 * time.Second},
		{raw: "30s", want: 30 * time.Second},
		{raw: "45s", want: 45 * time.Second},
		{raw: "1h", want: time.Hour},
	}
	r := &Reconciler{}
	for _, tt := range tests {
		t.Run("image/"+tt.raw, func(t *testing.T) {
			sub := &kardinalv1alpha1.Subscription{Spec: kardinalv1alpha1.SubscriptionSpec{
				Type:  kardinalv1alpha1.SubscriptionTypeImage,
				Image: &kardinalv1alpha1.ImageSubscriptionSpec{Registry: "ghcr.io/x/y", Interval: tt.raw},
			}}
			assert.Equal(t, tt.want, r.parseInterval(sub))
		})
		t.Run("git/"+tt.raw, func(t *testing.T) {
			sub := &kardinalv1alpha1.Subscription{Spec: kardinalv1alpha1.SubscriptionSpec{
				Type: kardinalv1alpha1.SubscriptionTypeGit,
				Git:  &kardinalv1alpha1.GitSubscriptionSpec{RepoURL: "https://example.com/r", Interval: tt.raw},
			}}
			assert.Equal(t, tt.want, r.parseInterval(sub))
		})
	}
}
