// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// TestBundleNameFor verifies Bundle names are DNS-safe, at most 63 characters,
// and carry a digest suffix so a re-pushed tag gets a new name (C04-gates-19).
func TestBundleNameFor(t *testing.T) {
	sha := "sha256:" + strings.Repeat("ab", 32)
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		sub    string
		result source.WatchResult
		want   string
	}{
		{name: "semver tag", sub: "app", result: source.WatchResult{Tag: "v1.2.3", Digest: sha}, want: "app-v1-2-3-abababab"},
		{name: "uppercase and underscore", sub: "app", result: source.WatchResult{Tag: "Build_42", Digest: sha}, want: "app-build-42-abababab"},
		{name: "git short sha is not repeated", sub: "cfg", result: source.WatchResult{Tag: "abc1234", Digest: "abc1234def5678"}, want: "cfg-abc1234d"},
		{name: "no digest falls back to time", sub: "app", result: source.WatchResult{Tag: "v1"}, want: "app-v1-20260413-100000"},
		{name: "long names are truncated", sub: strings.Repeat("s", 60), result: source.WatchResult{Tag: "v1", Digest: sha},
			want: strings.Repeat("s", 54) + "-abababab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bundleNameFor(tt.sub, &tt.result, now)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len(got), maxBundleNameLen)
			assert.Empty(t, validation.IsDNS1123Subdomain(got))
			assert.Empty(t, validation.IsValidLabelValue(got))
		})
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	sha := strings.Repeat("0123456789abcdef", 4)
	tests := []struct{ in, want string }{
		{in: "sha256:" + sha, want: sha[1:]},
		{in: "sha512:abc", want: "abc"},
		{in: "abc1234def", want: "abc1234def"},
		{in: "", want: ""},
		{in: "a/b+c", want: "abc"},
	}
	for _, tt := range tests {
		got := sanitizeLabelValue(tt.in)
		assert.Equal(t, tt.want, got, tt.in)
		assert.Empty(t, validation.IsValidLabelValue(got), tt.in)
	}
}
