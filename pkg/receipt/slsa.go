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
// Verified ImageVerification (docs/design/20-attestations.md): the lowest
// level of its images, as attestations.<name>.slsa.buildLevel folds them
// for a gate.
type SLSA struct {
	// BuildLevel is 0 to 3. An image's level is the highest of its verified
	// attestations that decide one (provenance from a builder the policy
	// grants a level, or a VSA); an image with none is 0.
	BuildLevel int `json:"buildLevel"`
	// LevelSource is builder or vsa: what decided the level, when every
	// image's level was decided the same way; left out otherwise.
	LevelSource string `json:"levelSource,omitempty"`
	// Images counts the images folded.
	Images int `json:"images"`
}

// slsaCheck is the SLSA build level of iv, nil when no attestation of iv
// decides one (its policy has no slsa): then the receipt claims no level.
// Only verified attestations count: a level an attestation claims that did
// not verify is never the receipt's.
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
	out := &SLSA{BuildLevel: -1, Images: len(images)}
	sources := map[string]bool{}
	for _, image := range images {
		level, source := 0, ""
		found := false
		for _, r := range iv.Status.Attestations {
			if r.Image != image || !r.Verified || r.SLSA == nil {
				continue
			}
			switch {
			case !found || r.SLSA.BuildLevel > level:
				level, source, found = r.SLSA.BuildLevel, r.SLSA.LevelSource, true
			case r.SLSA.BuildLevel == level && r.SLSA.LevelSource != source:
				source = ""
			}
		}
		if out.BuildLevel < 0 || level < out.BuildLevel {
			out.BuildLevel = level
		}
		sources[source] = true
	}
	if len(sources) == 1 {
		for s := range sources {
			out.LevelSource = s
		}
	}
	return out
}

// slsaLevels are the SLSA verifiedLevels of a record: SLSA_BUILD_LEVEL_<n>
// for a build level of at least 1 (the VSA's highest level of the track).
func slsaLevels(rec Record) []string {
	if s := rec.Checks.SLSA; s != nil && s.BuildLevel >= 1 {
		return []string{fmt.Sprintf("SLSA_BUILD_LEVEL_%d", s.BuildLevel)}
	}
	return nil
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
