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
// and carry a digest suffix so a re-pushed tag gets a new name (C04-gates-19),
// and a -<n> suffix for a digest that came back.
func TestBundleNameFor(t *testing.T) {
	sha := "sha256:" + strings.Repeat("ab", 32)
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		sub        string
		result     source.WatchResult
		generation int
		want       string
	}{
		{name: "semver tag", sub: "app", result: source.WatchResult{Tag: "v1.2.3", Digest: sha}, want: "app-v1-2-3-abababab"},
		{name: "uppercase and underscore", sub: "app", result: source.WatchResult{Tag: "Build_42", Digest: sha}, want: "app-build-42-abababab"},
		{name: "git short sha is not repeated", sub: "cfg", result: source.WatchResult{Tag: "abc1234", Digest: "abc1234def5678"}, want: "cfg-abc1234d"},
		{name: "no digest falls back to time", sub: "app", result: source.WatchResult{Tag: "v1"}, want: "app-v1-20260413-100000"},
		{name: "long names are truncated", sub: strings.Repeat("s", 60), result: source.WatchResult{Tag: "v1", Digest: sha},
			want: strings.Repeat("s", 54) + "-abababab"},
		{name: "generation 1 has no suffix", sub: "app", result: source.WatchResult{Tag: "main", Digest: sha}, generation: 1,
			want: "app-main-abababab"},
		{name: "a digest that came back gets its generation", sub: "app", result: source.WatchResult{Tag: "main", Digest: sha},
			generation: 2, want: "app-main-abababab-2"},
		{name: "long names are truncated before the generation", sub: strings.Repeat("s", 60),
			result: source.WatchResult{Tag: "v1", Digest: sha}, generation: 12, want: strings.Repeat("s", 51) + "-abababab-12"},
		{name: "no digest ignores the generation", sub: "app", result: source.WatchResult{Tag: "v1"}, generation: 3,
			want: "app-v1-20260413-100000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bundleNameFor(tt.sub, &tt.result, now, tt.generation)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len(got), maxBundleNameLen)
			assert.Empty(t, validation.IsDNS1123Subdomain(got))
			assert.Empty(t, validation.IsValidLabelValue(got))
		})
	}
}

// TestBundleGeneration verifies the generation read back from the name of a
// Bundle for a digest: n for a -<digest[:8]>-<n> name, 1 for any other name.
func TestBundleGeneration(t *testing.T) {
	sha := "sha256:" + strings.Repeat("ab", 32)
	tests := []struct {
		name string
		want int
	}{
		{name: "app-main-abababab", want: 1},
		{name: "app-main-abababab-2", want: 2},
		{name: "app-main-abababab-17", want: 17},
		{name: "app-20260413-095900", want: 1},
		{name: "app-main-abababab-x", want: 1},
		{name: "app-main-abababab-1", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bundleGeneration(tt.name, sha))
		})
	}
}

// TestSourceDigestLabel verifies the kardinal.io/source-digest label keeps the
// first 63 characters of the digest, so it is a prefix of the digest and of the
// 8-character short form in the Bundle name. It kept the last 63 and dropped
// the first hex character of a sha256 digest (E2E-R13).
func TestSourceDigestLabel(t *testing.T) {
	sha := strings.Repeat("0123456789abcdef", 4)
	tests := []struct{ name, in, want, wantLegacy string }{
		{name: "sha256 keeps a 63-char prefix", in: "sha256:" + sha, want: sha[:63], wantLegacy: sha[1:]},
		{name: "e2e digest", in: "sha256:34f9009520f4faa9bf1fcfc1d63a8d178e964e29d64019900abe41e4b613d04e",
			want:       "34f9009520f4faa9bf1fcfc1d63a8d178e964e29d64019900abe41e4b613d04",
			wantLegacy: "4f9009520f4faa9bf1fcfc1d63a8d178e964e29d64019900abe41e4b613d04e"},
		{name: "short digest is unchanged", in: "sha512:abc", want: "abc", wantLegacy: "abc"},
		{name: "git sha", in: "abc1234def", want: "abc1234def", wantLegacy: "abc1234def"},
		{name: "empty", in: "", want: "", wantLegacy: ""},
		{name: "invalid characters are dropped", in: "a/b+c", want: "abc", wantLegacy: "abc"},
		{name: "ends are trimmed after the cut", in: strings.Repeat("a", 62) + "-b", want: strings.Repeat("a", 62), wantLegacy: strings.Repeat("a", 61) + "-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourceDigestLabel(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Empty(t, validation.IsValidLabelValue(got))
			legacy := legacySourceDigestLabel(tt.in)
			assert.Equal(t, tt.wantLegacy, legacy)
			assert.Empty(t, validation.IsValidLabelValue(legacy))
			assert.True(t, sourceDigestMatches(got, tt.in))
			assert.True(t, sourceDigestMatches(legacy, tt.in), "a Bundle labelled by an older controller still matches")
		})
	}
	assert.False(t, sourceDigestMatches(sha[:63], "sha256:"+strings.Repeat("f", 64)))
}
