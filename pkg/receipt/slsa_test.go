// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// bundleOf are the Bundle images of these repo@digest (or repo:tag)
// references.
func bundleOf(refs ...string) []v1alpha1.ImageRef {
	var out []v1alpha1.ImageRef
	for _, r := range refs {
		if repo, digest, ok := strings.Cut(r, "@"); ok {
			out = append(out, v1alpha1.ImageRef{Repository: repo, Digest: digest})
			continue
		}
		i := strings.LastIndexByte(r, ':')
		out = append(out, v1alpha1.ImageRef{Repository: r[:i], Tag: r[i+1:]})
	}
	return out
}

// TestSLSACheck (#2061): the receipt's SLSA build level is the lowest of
// its images', as #1963 folds attestations.<name>.slsa.buildLevel; an
// image's level is its highest verified one, 0 without one; an attestation
// that did not verify never counts; a policy that decides no level gives
// none; the level source is kept only when every image's agrees.
func TestSLSACheck(t *testing.T) {
	one := []string{imgA}
	two := []string{imgA, imgB}
	lvl := func(n int) *int { return &n }
	verified := func(img string, n int, source string) SLSAImage {
		return SLSAImage{Image: img, Status: SLSAVerified, BuildLevel: lvl(n), LevelSource: source}
	}
	none := func(img string) SLSAImage { return SLSAImage{Image: img, Status: SLSANone} }
	cases := map[string]struct {
		iv   *v1alpha1.ImageVerification
		want *SLSA
	}{
		"no ImageVerification":  {nil, nil},
		"no slsa in the policy": {ivWith(one, v1alpha1.AttestationResult{Name: "sbom", Image: imgA, Verified: true}), nil},
		"level 0, an unlisted builder": {ivWith(one, slsaResult("provenance", imgA, true, 0, "builder")),
			&SLSA{BuildLevel: 0, LevelSource: "builder", Images: []SLSAImage{verified(imgA, 0, "builder")}}},
		"level 3 from the builder": {ivWith(one, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 3, LevelSource: "builder", Images: []SLSAImage{verified(imgA, 3, "builder")}}},
		"not verified: none": {ivWith(one, slsaResult("provenance", imgA, false, 3, "builder")),
			&SLSA{BuildLevel: 0, Images: []SLSAImage{none(imgA)}}},
		"two images: the lowest": {ivWith(two, slsaResult("provenance", imgA, true, 3, "builder"), slsaResult("provenance", imgB, true, 1, "builder")),
			&SLSA{BuildLevel: 1, LevelSource: "builder", Images: []SLSAImage{verified(imgA, 3, "builder"), verified(imgB, 1, "builder")}}},
		"an image without one: none, level 0": {ivWith(two, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 0, Images: []SLSAImage{verified(imgA, 3, "builder"), none(imgB)}}},
		"an image's best of two policies": {ivWith(one, slsaResult("provenance", imgA, true, 1, "builder"), slsaResult("vsa", imgA, true, 3, "vsa")),
			&SLSA{BuildLevel: 3, LevelSource: "vsa", Images: []SLSAImage{verified(imgA, 3, "vsa")}}},
		"sources that differ are left out": {ivWith(two, slsaResult("provenance", imgA, true, 2, "builder"), slsaResult("vsa", imgB, true, 2, "vsa")),
			&SLSA{BuildLevel: 2, Images: []SLSAImage{verified(imgA, 2, "builder"), verified(imgB, 2, "vsa")}}},
		// QA-2 #2169: a Bundle image the policy's images do not select is
		// never in the ImageVerification, and is a subject all the same.
		"a Bundle image the policy does not select: none, level 0": {ivWith(one, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 0, Images: []SLSAImage{verified(imgA, 3, "builder"), none(imgB)}}},
		"the same repository at another digest: none": {ivWith(one, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 0, Images: []SLSAImage{verified(imgA, 3, "builder"), none("ghcr.io/acme/checkout@sha256:cccc")}}},
		"a tag-only Bundle image is not a subject": {ivWith(one, slsaResult("provenance", imgA, true, 3, "builder")),
			&SLSA{BuildLevel: 3, LevelSource: "builder", Images: []SLSAImage{verified(imgA, 3, "builder")}}},
		"a Bundle repository named as the ImageVerification normalizes it": {
			ivWith([]string{"docker.io/library/nginx@sha256:dddd"}, slsaResult("provenance", "docker.io/library/nginx@sha256:dddd", true, 2, "builder")),
			&SLSA{BuildLevel: 2, LevelSource: "builder", Images: []SLSAImage{verified("docker.io/library/nginx@sha256:dddd", 2, "builder")}}},
	}
	// The Bundle's images, when not the ImageVerification's.
	bundles := map[string][]v1alpha1.ImageRef{
		"a Bundle image the policy does not select: none, level 0":         bundleOf(imgA, imgB),
		"the same repository at another digest: none":                      bundleOf(imgA, "ghcr.io/acme/checkout@sha256:cccc"),
		"a tag-only Bundle image is not a subject":                         bundleOf(imgA, "ghcr.io/acme/worker:1.0"),
		"a Bundle repository named as the ImageVerification normalizes it": bundleOf("nginx@sha256:dddd"),
	}
	// QA-2 #2169: the receipt's subjects that are not images.
	commit := Subject{Name: "https://git.example.com/acme/deploy", Digest: map[string]string{"gitCommit": "3d1e"}}
	cases["a config commit subject: none, level 0"] = struct {
		iv   *v1alpha1.ImageVerification
		want *SLSA
	}{ivWith(one, slsaResult("provenance", imgA, true, 3, "builder")),
		&SLSA{BuildLevel: 0, Images: []SLSAImage{verified(imgA, 3, "builder")},
			OtherSubjects: []SLSASubject{{Name: commit.Name, Digest: commit.Digest, Status: SLSANone}}}}
	others := map[string][]Subject{"a config commit subject: none, level 0": {commit}}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bundle, ok := bundles[name]
			if !ok && tc.iv != nil {
				for _, r := range tc.iv.Status.Images {
					bundle = append(bundle, bundleOf(r.Image)...)
				}
			}
			assert.Equal(t, tc.want, slsaCheck(tc.iv, bundle, others[name]))
		})
	}
}

// TestBuild_SLSAMixedBundle (QA-2 #2169): a version of an image whose
// provenance verified at level 3 and one with no verified attestation (an
// unverified one, here) records the first's level and "none" for the
// second, and claims no SLSA level: never one for an unchecked image.
func TestBuild_SLSAMixedBundle(t *testing.T) {
	in := input("image")
	sidecar := "ghcr.io/acme/sidecar@sha256:" + strings.Repeat("b", 64)
	in.Bundle.Spec.Images = append(in.Bundle.Spec.Images, bundleOf(sidecar)...)
	in.ImageVerification = ivWith([]string{"ghcr.io/acme/checkout@" + imageDigest, sidecar},
		slsaResult("provenance", "ghcr.io/acme/checkout@"+imageDigest, true, 3, "builder"),
		slsaResult("provenance", sidecar, false, 3, "builder"))
	raw, _, err := Build(in)
	require.NoError(t, err)
	st, err := Parse(raw)
	require.NoError(t, err)
	got := st.Predicate.Kardinal.Checks.SLSA
	require.NotNil(t, got)
	require.Len(t, got.Images, 2)
	assert.Equal(t, SLSAVerified, got.Images[0].Status)
	require.NotNil(t, got.Images[0].BuildLevel)
	assert.Equal(t, 3, *got.Images[0].BuildLevel)
	assert.Equal(t, SLSAImage{Image: sidecar, Status: SLSANone}, got.Images[1])
	assert.Equal(t, 0, got.BuildLevel)
	for _, l := range st.Predicate.VerifiedLevels {
		assert.False(t, strings.HasPrefix(l, "SLSA_"), "no SLSA level claimed: %s", l)
	}
	assert.Contains(t, string(raw), `"status":"none"`)
}

// unselectedInput is the test promotion whose Bundle also has
// docker.io/thirdparty/sidecar, an image the Pipeline's
// imageVerification.images does not select: the ImageVerification holds
// checkout only, whose provenance verified at level 3.
func unselectedInput() (Input, string) {
	in := input("image")
	digest := "sha256:" + strings.Repeat("c", 64)
	in.Bundle.Spec.Images = append(in.Bundle.Spec.Images, v1alpha1.ImageRef{Repository: "docker.io/thirdparty/sidecar", Digest: digest})
	checkout := "ghcr.io/acme/checkout@" + imageDigest
	in.ImageVerification = ivWith([]string{checkout}, slsaResult("provenance", checkout, true, 3, "builder"))
	return in, digest
}

// TestBuild_SLSAUnselectedImage (QA-2 #2169): a Bundle image the policy
// does not select is a subject of the receipt but never in the
// ImageVerification: it is recorded none, the version's level is 0, and no
// SLSA level is claimed (verifiedLevels hold for every subject).
func TestBuild_SLSAUnselectedImage(t *testing.T) {
	in, digest := unselectedInput()
	raw, _, err := Build(in)
	require.NoError(t, err)
	st, err := Parse(raw)
	require.NoError(t, err)
	require.Len(t, st.Subject, 2, "the sidecar is a subject")
	got := st.Predicate.Kardinal.Checks.SLSA
	require.NotNil(t, got)
	require.Len(t, got.Images, 2)
	assert.Equal(t, SLSAVerified, got.Images[0].Status)
	assert.Equal(t, SLSAImage{Image: "docker.io/thirdparty/sidecar@" + digest, Status: SLSANone}, got.Images[1])
	assert.Equal(t, 0, got.BuildLevel)
	for _, l := range st.Predicate.VerifiedLevels {
		assert.False(t, strings.HasPrefix(l, "SLSA_"), "no SLSA level claimed: %s", l)
	}
}

// TestBuild_SLSAMixedVersionCommit (QA-2 #2169): a mixed version's config
// commit is a subject of the receipt that no SLSA attestation covers: it is
// recorded none, the version's level is 0, no SLSA level is claimed, and
// verify-receipt --subject <the commit> --require SLSA_BUILD_LEVEL_3 fails,
// however well its image was built.
func TestBuild_SLSAMixedVersionCommit(t *testing.T) {
	in := input("mixed")
	checkout := "ghcr.io/acme/checkout@" + imageDigest
	in.ImageVerification = ivWith([]string{checkout}, slsaResult("provenance", checkout, true, 3, "builder"))
	raw, _, err := Build(in)
	require.NoError(t, err)
	st, err := Parse(raw)
	require.NoError(t, err)
	got := st.Predicate.Kardinal.Checks.SLSA
	require.NotNil(t, got)
	require.Len(t, got.Images, 1)
	assert.Equal(t, SLSAVerified, got.Images[0].Status, "the image's own level is recorded")
	assert.Equal(t, []SLSASubject{{Name: "https://git.example.com/acme/deploy", Digest: map[string]string{"gitCommit": "3d1e"},
		Status: SLSANone}}, got.OtherSubjects)
	assert.Equal(t, 0, got.BuildLevel)
	for _, l := range st.Predicate.VerifiedLevels {
		assert.False(t, strings.HasPrefix(l, "SLSA_"), "no SLSA level claimed: %s", l)
	}

	s, pub := keySigner(t)
	b, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	_, err = Verify(b.Bundle, VerifyOptions{Key: pub, Subject: "3d1e"})
	require.NoError(t, err, "the commit is a subject")
	_, err = Verify(b.Bundle, VerifyOptions{Key: pub, Subject: "3d1e", Require: []string{"SLSA_BUILD_LEVEL_3"}})
	require.Error(t, err, "a commit never attested is never level 3")
	assert.Contains(t, err.Error(), "SLSA_BUILD_LEVEL_3")
	_, err = Verify(b.Bundle, VerifyOptions{Key: pub, Subject: imageDigest, Require: []string{"SLSA_BUILD_LEVEL_3"}})
	require.Error(t, err, "the version-level claim covers every subject: none is made")
}

// TestBuild_SLSABundleSpecSubject (QA-2 #2169): a version named by its
// Bundle's spec (no image digest, no commit) has a subject no attestation
// covers: it is none and no level is claimed.
func TestBuild_SLSABundleSpecSubject(t *testing.T) {
	in := input("image")
	in.Bundle.Spec.Images[0].Digest, in.Bundle.Spec.Images[0].Tag = "", "1.5.0"
	checkout := "ghcr.io/acme/checkout@" + imageDigest
	in.ImageVerification = ivWith([]string{checkout}, slsaResult("provenance", checkout, true, 3, "builder"))
	st, _ := build(t, in)
	got := st.Predicate.Kardinal.Checks.SLSA
	require.NotNil(t, got)
	require.Len(t, got.OtherSubjects, 1)
	assert.True(t, strings.HasPrefix(got.OtherSubjects[0].Name, "kardinal://"), got.OtherSubjects[0].Name)
	assert.Equal(t, SLSANone, got.OtherSubjects[0].Status)
	assert.Equal(t, 0, got.BuildLevel)
	for _, l := range st.Predicate.VerifiedLevels {
		assert.False(t, strings.HasPrefix(l, "SLSA_"), "no SLSA level claimed: %s", l)
	}
}

// TestUncoveredSubjects (QA-2 #2169): a subject is uncovered unless it is
// one of the Bundle's digest-pinned images, the ones the SLSA fold checks:
// the config commit, the Bundle's spec, and a kind of subject kardinal
// does not know (a future one) are all uncovered, so a new kind fails
// closed; an image digest the Bundle does not pin is uncovered too.
func TestUncoveredSubjects(t *testing.T) {
	images := bundleOf("ghcr.io/acme/checkout@sha256:aaaa", "ghcr.io/acme/worker:1.0")
	image := Subject{Name: "ghcr.io/acme/checkout", Digest: map[string]string{"sha256": "aaaa"}}
	commit := Subject{Name: "https://git.example.com/acme/deploy", Digest: map[string]string{"gitCommit": "3d1e"}}
	spec := Subject{Name: "kardinal://c1/namespaces/default/bundles/b", Digest: map[string]string{"sha256": "bbbb"}}
	future := Subject{Name: "oci://charts.example.com/checkout", Digest: map[string]string{"sha512": "cccc"}}
	otherDigest := Subject{Name: "ghcr.io/acme/checkout", Digest: map[string]string{"sha256": "dddd"}}
	twoDigests := Subject{Name: "ghcr.io/acme/checkout", Digest: map[string]string{"sha256": "aaaa", "sha512": "eeee"}}
	got := uncovered([]Subject{image, commit, spec, future, otherDigest, twoDigests}, images)
	assert.Equal(t, []Subject{commit, spec, future, otherDigest, twoDigests}, got)
	assert.Empty(t, uncovered([]Subject{image}, images), "a checked image is covered")

	// A version with a subject of a kind kardinal does not know claims no
	// level, however well its image was built.
	checkout := "ghcr.io/acme/checkout@sha256:aaaa"
	s := slsaCheck(ivWith([]string{checkout}, slsaResult("provenance", checkout, true, 3, "builder")),
		bundleOf(checkout), uncovered([]Subject{image, future}, bundleOf(checkout)))
	require.NotNil(t, s)
	assert.Equal(t, 0, s.BuildLevel)
	assert.Equal(t, []SLSASubject{{Name: future.Name, Digest: future.Digest, Status: SLSANone}}, s.OtherSubjects)
	assert.Empty(t, slsaLevels(Record{Checks: Checks{SLSA: s}}))
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
			in.Bundle.Spec.Images = append(in.Bundle.Spec.Images, bundleOf(img)...)
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
	// QA-2 #2169: verify-receipt --image <an image the policy does not
	// select> --require SLSA_BUILD_LEVEL_1 fails; checkout's level 3 is not
	// the sidecar's.
	t.Run("an unselected image require SLSA_BUILD_LEVEL_1", func(t *testing.T) {
		in, digest := unselectedInput()
		raw, _, err := Build(in)
		require.NoError(t, err)
		b, err := s.Sign(context.Background(), raw)
		require.NoError(t, err)
		_, err = Verify(b.Bundle, VerifyOptions{Key: pub, Subject: digest})
		require.NoError(t, err, "the sidecar is a subject")
		_, err = Verify(b.Bundle, VerifyOptions{Key: pub, Subject: digest, Require: []string{"SLSA_BUILD_LEVEL_1"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SLSA_BUILD_LEVEL_1")
	})
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
