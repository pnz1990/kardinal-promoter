// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestCapPRBody: a body over the limit is cut to exactly maxPRBody
// characters, its last kept character whole: with a three-byte character
// across the cut, at every offset, so a cut by bytes would split one
// (invalid UTF-8) or keep fewer characters. Covers SCM-PRBODY-CAP-01.
func TestCapPRBody(t *testing.T) {
	keep := maxPRBody - utf8.RuneCountInString(prBodyCut)
	for pad := 0; pad < 3; pad++ {
		// keep characters before the cut: pad ASCII ones, then "€" (3 bytes).
		body := strings.Repeat("a", pad) + strings.Repeat("€", keep-pad) + strings.Repeat("€", 1000)
		got := capPRBody(body)
		require.True(t, utf8.ValidString(got), "pad %d: cut inside a character", pad)
		assert.Equal(t, maxPRBody, utf8.RuneCountInString(got), "pad %d: the cut body is exactly the limit", pad)
		assert.Equal(t, strings.Repeat("a", pad)+strings.Repeat("€", keep-pad)+prBodyCut, got, "pad %d", pad)
	}
	fits := strings.Repeat("€", maxPRBody)
	assert.Equal(t, fits, capPRBody(fits), "a body at the limit is untouched")
}

// TestRenderPR_Capped: whatever the version carries (an author the CRD let
// through before it bounded the field), the PR body is at most 65,536
// characters, GitHub's limit, with a note when cut, for the default body and
// for a pr.bodyTemplate (bounded in bytes, which a marker and multi-byte
// text still pass). Covers SCM-PRBODY-CAP-01.
func TestRenderPR_Capped(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: "1.0.0"}, {Repository: "ghcr.io/acme/app", Tag: "1.0.1"},
		{Repository: "ghcr.io/acme/app", Tag: "1.0.2"}}
	long := func(author string) PRBody {
		return PRBody{PipelineName: "app", Environment: "prod",
			Bundle: v1alpha1.BundleSpec{Type: "image", Images: images, Provenance: &v1alpha1.BundleProvenance{Author: author}}}
	}
	// A template reads each value cut to maxTemplateValue characters and
	// writes at most 64 KiB, so it fills the limit by repeating one; the
	// marker then takes it past.
	for name, tc := range map[string]struct {
		cfg    *v1alpha1.PRConfig
		author string
	}{
		"default":  {nil, strings.Repeat("a", 65530)},
		"template": {&v1alpha1.PRConfig{BodyTemplate: strings.Repeat("{{ .Bundle.Author }}", 64)}, strings.Repeat("a", maxTemplateValue)},
	} {
		cfg := tc.cfg
		got, err := RenderPR(cfg, "title", long(tc.author))
		require.NoError(t, err, name)
		assert.LessOrEqual(t, utf8.RuneCountInString(got.Body), maxPRBody, name)
		assert.True(t, strings.HasPrefix(got.Body, "<!-- kardinal-promoter auto-generated PR -->"), "%s: the marker kardinal finds its PRs by stays", name)
		assert.True(t, strings.HasSuffix(got.Body, prBodyCut), "%s: the body says it was cut", name)
		assert.True(t, utf8.ValidString(got.Body), name)

		short, err := RenderPR(cfg, "title", long("ci"))
		require.NoError(t, err, name)
		assert.NotContains(t, short.Body, "was cut", name)
	}
}
