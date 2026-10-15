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

// TestRenderPRBody_Capped: whatever text the version carries (an author the
// CRD let through before it bounded the field, many images), the default
// body is at most 65,536 characters, GitHub's limit, and says it was cut;
// a body that fits is not touched. Covers SCM-PRBODY-CAP-01.
func TestRenderPRBody_Capped(t *testing.T) {
	long := strings.Repeat("é", 70000)
	images := make([]v1alpha1.ImageRef, 0, 3)
	for _, tag := range []string{"1.0.0", "1.0.1", "1.0.2"} {
		images = append(images, v1alpha1.ImageRef{Repository: "ghcr.io/acme/app", Tag: tag})
	}
	body, err := RenderPRBody(PRBody{PipelineName: "app", Environment: "prod",
		Bundle: v1alpha1.BundleSpec{Type: "image", Images: images, Provenance: &v1alpha1.BundleProvenance{Author: long}}})
	require.NoError(t, err)
	assert.LessOrEqual(t, utf8.RuneCountInString(body), maxPRBody)
	assert.True(t, strings.HasPrefix(body, "<!-- kardinal-promoter auto-generated PR -->"), "the marker kardinal finds its PRs by stays")
	assert.True(t, strings.HasSuffix(body, prBodyCut), "the body says it was cut")
	assert.True(t, utf8.ValidString(body), "cut on a character boundary")

	short, err := RenderPRBody(PRBody{PipelineName: "app", Environment: "prod",
		Bundle: v1alpha1.BundleSpec{Type: "image", Images: images[:1], Provenance: &v1alpha1.BundleProvenance{Author: "ci"}}})
	require.NoError(t, err)
	assert.NotContains(t, short, "was cut")
}
