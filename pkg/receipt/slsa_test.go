// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	slsaProvenance = "https://slsa.dev/provenance/v1"
	slsaVSA        = "https://slsa.dev/verification_summary/v1"
)

// slsaResult is one image's SLSA attestation result.
func slsaResult(name, image string, verified bool, level int, source string) v1alpha1.AttestationResult {
	pt := slsaProvenance
	if source == "vsa" {
		pt = slsaVSA
	}
	return v1alpha1.AttestationResult{Name: name, Image: image, PredicateType: pt, Verified: verified,
		SLSA: &v1alpha1.SLSAResult{BuildLevel: level, LevelSource: source}}
}

// ivWith is a Verified ImageVerification of images with these attestation
// results.
func ivWith(images []string, results ...v1alpha1.AttestationResult) *v1alpha1.ImageVerification {
	iv := &v1alpha1.ImageVerification{Status: v1alpha1.ImageVerificationStatus{Phase: v1alpha1.ImageVerificationVerified,
		Attestations: results}}
	for _, img := range images {
		iv.Status.Images = append(iv.Status.Images, v1alpha1.ImageVerificationResult{Image: img, Verified: true, Signer: "ci"})
	}
	return iv
}

const (
	imgA = "ghcr.io/acme/checkout@sha256:aaaa"
	imgB = "ghcr.io/acme/worker@sha256:bbbb"
)

// TestSLSACheck (#2061): the receipt's SLSA build level is the lowest of
// its images', as #1963 folds attestations.<name>.slsa.buildLevel; an
// image's level is its highest verified one, 0 without one; an attestation
// that did not verify never counts; a policy that decides no level gives
// none; the level source is kept only when every image's agrees.
func TestSLSACheck(t *testing.T) {
	one := []string{imgA}
	two := []string{imgA, imgB}
	cases := map[string]struct {
		iv   *v1alpha1.ImageVerification
		want *SLSA
	}{
		"no ImageVerification":  {nil, nil},
		"no slsa in the policy": {ivWith(one, v1alpha1.AttestationResult{Name: "sbom", Image: imgA, Verified: true}), nil},
		"level 0, an unlisted builder": {ivWith(one, slsaResult("provenance", imgA, true, 0, "builder")),
			&SLSA{BuildLevel: 0, LevelSource: "builder", Images: 1}},
		"level 3 from the builder": {ivWith(one, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 3, LevelSource: "builder", Images: 1}},
		"not verified: level 0": {ivWith(one, slsaResult("provenance", imgA, false, 3, "builder")),
			&SLSA{BuildLevel: 0, Images: 1}},
		"two images: the lowest": {ivWith(two, slsaResult("provenance", imgA, true, 3, "builder"), slsaResult("provenance", imgB, true, 1, "builder")),
			&SLSA{BuildLevel: 1, LevelSource: "builder", Images: 2}},
		"an image without one: 0": {ivWith(two, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 0, Images: 2}},
		"an image's best of two policies": {ivWith(one, slsaResult("provenance", imgA, true, 1, "builder"), slsaResult("vsa", imgA, true, 3, "vsa")),
			&SLSA{BuildLevel: 3, LevelSource: "vsa", Images: 1}},
		"sources that differ are left out": {ivWith(two, slsaResult("provenance", imgA, true, 2, "builder"), slsaResult("vsa", imgB, true, 2, "vsa")),
			&SLSA{BuildLevel: 2, Images: 2}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, slsaCheck(tc.iv))
		})
	}
}

// slsaInput is the test promotion with an ImageVerification whose images
// have these build levels from their builder.
func slsaInput(levels ...int) Input {
	in := input("image")
	var images []string
	var results []v1alpha1.AttestationResult
	for i, l := range levels {
		img := "ghcr.io/acme/checkout@" + imageDigest
		if i > 0 {
			img = fmt.Sprintf("ghcr.io/acme/sidecar-%d@sha256:%064d", i, i)
		}
		images = append(images, img)
		results = append(results, slsaResult("provenance", img, true, l, "builder"))
	}
	in.ImageVerification = ivWith(images, results...)
	return in
}

// TestBuild_SLSAGoldenStatements (#2061): the statement at build levels 0
// to 3 and with several images, byte for byte (go test -update rewrites
// testdata): checks.slsa records the level, its source and the images
// folded, and verifiedLevels claims SLSA_BUILD_LEVEL_<n> for a level of at
// least 1, the lowest image's.
//
// Covers RECEIPT-STATEMENT-05.
func TestBuild_SLSAGoldenStatements(t *testing.T) {
	cases := map[string]struct {
		in    Input
		level string
	}{
		"statement-slsa-level-0": {slsaInput(0), ""},
		"statement-slsa-level-1": {slsaInput(1), "SLSA_BUILD_LEVEL_1"},
		"statement-slsa-level-2": {slsaInput(2), "SLSA_BUILD_LEVEL_2"},
		"statement-slsa-level-3": {slsaInput(3), "SLSA_BUILD_LEVEL_3"},
		"statement-slsa-images":  {slsaInput(3, 2, 3), "SLSA_BUILD_LEVEL_2"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _, err := Build(tc.in)
			require.NoError(t, err)
			var pretty map[string]interface{}
			require.NoError(t, json.Unmarshal(raw, &pretty))
			got, err := json.MarshalIndent(pretty, "", "  ")
			require.NoError(t, err)
			path := filepath.Join("testdata", name+".json")
			if *updateGolden {
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got)+"\n")

			st, err := Parse(raw)
			require.NoError(t, err)
			var slsa []string
			for _, l := range st.Predicate.VerifiedLevels {
				if slsaLevel.MatchString(l) {
					slsa = append(slsa, l)
				}
			}
			if tc.level == "" {
				assert.Empty(t, slsa, "no SLSA level below 1")
			} else {
				assert.Equal(t, []string{tc.level}, slsa)
			}
		})
	}
	plain, _, err := Build(input("image"))
	require.NoError(t, err)
	st, err := Parse(plain)
	require.NoError(t, err)
	assert.Nil(t, st.Predicate.Kardinal.Checks.SLSA, "a policy without slsa decides no level")
}

// TestVerify_RequireSLSA (#2061): verify-receipt --require
// SLSA_BUILD_LEVEL_<n> admits a receipt at level n or above (a VSA lists a
// track's highest level) and refuses one below; !SLSA_BUILD_LEVEL_<n>
// refuses a receipt at n or above. Other levels match exactly.
func TestVerify_RequireSLSA(t *testing.T) {
	s, pub := keySigner(t)
	signed := map[int][]byte{}
	for _, l := range []int{0, 1, 3} {
		raw, _, err := Build(slsaInput(l))
		require.NoError(t, err)
		b, err := s.Sign(context.Background(), raw)
		require.NoError(t, err)
		signed[l] = b.Bundle
	}
	for _, tc := range []struct {
		level   int
		require string
		ok      bool
	}{
		{3, "SLSA_BUILD_LEVEL_3", true},
		{3, "SLSA_BUILD_LEVEL_2", true},
		{3, "SLSA_BUILD_LEVEL_1", true},
		{1, "SLSA_BUILD_LEVEL_3", false},
		{1, "SLSA_BUILD_LEVEL_2", false},
		{1, "SLSA_BUILD_LEVEL_1", true},
		{0, "SLSA_BUILD_LEVEL_1", false},
		{3, "!SLSA_BUILD_LEVEL_3", false},
		{3, "!SLSA_BUILD_LEVEL_2", false},
		{1, "!SLSA_BUILD_LEVEL_2", true},
		{0, "!SLSA_BUILD_LEVEL_1", true},
		{3, "SLSA_SOURCE_LEVEL_1", false},
	} {
		t.Run(fmt.Sprintf("level %d require %s", tc.level, tc.require), func(t *testing.T) {
			_, err := Verify(signed[tc.level], VerifyOptions{Key: pub, Require: []string{tc.require}})
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestHasLevel: a SLSA track level verifies the lower levels of its track
// only; any other level matches exactly.
func TestHasLevel(t *testing.T) {
	levels := []string{LevelGatesPassed, "SLSA_BUILD_LEVEL_2"}
	assert.True(t, hasLevel(levels, LevelGatesPassed))
	assert.False(t, hasLevel(levels, LevelApproved))
	assert.True(t, hasLevel(levels, "SLSA_BUILD_LEVEL_1"))
	assert.True(t, hasLevel(levels, "SLSA_BUILD_LEVEL_2"))
	assert.False(t, hasLevel(levels, "SLSA_BUILD_LEVEL_3"))
	assert.False(t, hasLevel(levels, "SLSA_SOURCE_LEVEL_1"))
	assert.False(t, hasLevel([]string{"KARDINAL_LEVEL_9"}, "SLSA_BUILD_LEVEL_1"))
}
