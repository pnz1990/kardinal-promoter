// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// SLSA is the SLSA build level of the promoted version, from its newest
// Verified ImageVerification (docs/design/20-attestations.md), image by
// image: an image's level is recorded only when one of its attestations
// that decide a level verified; every other image is recorded "none". The
// version's level is the lowest of its images' and is claimed only when
// every image has one: a receipt never claims a level for an image whose
// level was not checked.
type SLSA struct {
	// BuildLevel is 0 to 3: the lowest image's level, 0 when an image has
	// none.
	BuildLevel int `json:"buildLevel"`
	// LevelSource is builder or vsa: what decided the level, when every
	// image's level was decided the same way; left out otherwise.
	LevelSource string `json:"levelSource,omitempty"`
	// Images is each image's level, in the ImageVerification's order.
	Images []SLSAImage `json:"images"`
}

// SLSAImage is one image's SLSA build level.
type SLSAImage struct {
	Image string `json:"image"`
	// Status is verified (an attestation that decides a level verified for
	// the image) or none.
	Status string `json:"status"`
	// BuildLevel is the highest level of the image's verified attestations;
	// left out when Status is none.
	BuildLevel *int `json:"buildLevel,omitempty"`
	// LevelSource is builder or vsa, when Status is verified.
	LevelSource string `json:"levelSource,omitempty"`
}

// The SLSAImage statuses.
const (
	SLSAVerified = "verified"
	SLSANone     = "none"
)

// slsaCheck is the SLSA build level of iv, image by image; nil when no
// attestation of iv decides one (its policy has no slsa): then the receipt
// claims no level. Only verified attestations count: a level an
// attestation claims that did not verify is never the receipt's.
func slsaCheck(iv *v1alpha1.ImageVerification) *SLSA {
	if iv == nil {
		return nil
	}
	decides := false
	var images []string
	seen := map[string]bool{}
	add := func(image string) {
		if !seen[image] {
			seen[image] = true
			images = append(images, image)
		}
	}
	for _, r := range iv.Status.Images {
		add(r.Image)
	}
	for _, r := range iv.Status.Attestations {
		if r.SLSA != nil {
			decides = true
		}
		add(r.Image)
	}
	if !decides || len(images) == 0 {
		return nil
	}
	out := &SLSA{BuildLevel: -1}
	sources := map[string]bool{}
	for _, image := range images {
		img := SLSAImage{Image: image, Status: SLSANone}
		level, source := 0, ""
		for _, r := range iv.Status.Attestations {
			if r.Image != image || !r.Verified || r.SLSA == nil {
				continue
			}
			switch {
			case img.Status == SLSANone || r.SLSA.BuildLevel > level:
				level, source, img.Status = r.SLSA.BuildLevel, r.SLSA.LevelSource, SLSAVerified
			case r.SLSA.BuildLevel == level && r.SLSA.LevelSource != source:
				source = ""
			}
		}
		if img.Status == SLSAVerified {
			l := level
			img.BuildLevel, img.LevelSource = &l, source
		} else {
			level = 0
		}
		if out.BuildLevel < 0 || level < out.BuildLevel {
			out.BuildLevel = level
		}
		sources[img.LevelSource] = true
		out.Images = append(out.Images, img)
	}
	if len(sources) == 1 {
		for s := range sources {
			out.LevelSource = s
		}
	}
	return out
}

// slsaLevels are the SLSA verifiedLevels of a record: SLSA_BUILD_LEVEL_<n>
// for a build level of at least 1 (the VSA's highest level of the track)
// when every image's level was verified.
func slsaLevels(rec Record) []string {
	// An image recorded none makes the build level 0: a level is never
	// claimed for an unchecked image.
	s := rec.Checks.SLSA
	if s == nil || s.BuildLevel < 1 {
		return nil
	}
	return []string{fmt.Sprintf("SLSA_BUILD_LEVEL_%d", s.BuildLevel)}
}

// slsaLevel matches a SLSA track level, SLSA_<TRACK>_LEVEL_<n>.
var slsaLevel = regexp.MustCompile(`^SLSA_([A-Z]+)_LEVEL_([0-9]+)$`)

// hasLevel reports whether levels verify want. A VSA lists the highest
// level of a SLSA track, which verifies every lower one: SLSA_BUILD_LEVEL_3
// verifies a required SLSA_BUILD_LEVEL_2. Any other level is listed as is.
func hasLevel(levels []string, want string) bool {
	m := slsaLevel.FindStringSubmatch(want)
	for _, l := range levels {
		if l == want {
			return true
		}
		if m == nil {
			continue
		}
		if g := slsaLevel.FindStringSubmatch(l); g != nil && g[1] == m[1] {
			have, _ := strconv.Atoi(g[2])
			need, _ := strconv.Atoi(m[2])
			if have >= need {
				return true
			}
		}
	}
	return false
}
