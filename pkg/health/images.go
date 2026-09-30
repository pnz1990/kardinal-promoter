// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"fmt"
	"strings"
)

// ImageExpectation is one Bundle image a workload must run.
type ImageExpectation struct {
	// Repository is the image repository, e.g. "ghcr.io/org/app".
	Repository string
	// Tag is the expected tag, e.g. "sha-1a2b3c4". May be empty when Digest is set.
	Tag string
	// Digest is the expected digest, e.g. "sha256:...". Optional.
	Digest string
}

// ParseImage splits an image reference such as
// "registry:5000/org/app:v1@sha256:abc" into repository, tag and digest.
func ParseImage(ref string) ImageExpectation {
	var img ImageExpectation
	if i := strings.Index(ref, "@"); i >= 0 {
		img.Digest = ref[i+1:]
		ref = ref[:i]
	}
	// A tag separator is a ':' after the last '/'; an earlier ':' is a registry port.
	if c := strings.LastIndex(ref, ":"); c > strings.LastIndex(ref, "/") {
		img.Tag = ref[c+1:]
		ref = ref[:c]
	}
	img.Repository = ref
	return img
}

// normalizeRepository makes Docker Hub short names comparable:
// "docker.io/library/nginx", "index.docker.io/nginx" and "nginx" are equal.
func normalizeRepository(repo string) string {
	r := strings.ToLower(repo)
	for _, prefix := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		if strings.HasPrefix(r, prefix) {
			r = strings.TrimPrefix(r, prefix)
			break
		}
	}
	return strings.TrimPrefix(r, "library/")
}

// imageMatches reports whether a running image satisfies an expectation of the
// same repository. Digests win when both sides have one; otherwise the tag is
// compared. An expectation with only a digest cannot be met by a tag.
func imageMatches(want, got ImageExpectation) bool {
	if want.Digest != "" && got.Digest != "" {
		return want.Digest == got.Digest
	}
	if want.Tag != "" {
		tag := got.Tag
		if tag == "" && got.Digest == "" {
			tag = "latest"
		}
		return tag == want.Tag
	}
	return want.Digest == ""
}

// checkImages compares the images a workload runs with the Bundle images.
//
// Every running image whose repository is a Bundle repository must match that
// Bundle image; a mismatch returns false with a description. A workload that
// runs none of the Bundle repositories (for example a Bundle image renamed by
// kustomize newName) cannot be verified this way: the check passes with a
// note so the status message shows the image was not verified.
func checkImages(expected []ImageExpectation, running []string) (bool, string) {
	if len(expected) == 0 {
		return true, ""
	}
	present := false
	for _, want := range expected {
		repo := normalizeRepository(want.Repository)
		for _, ref := range running {
			got := ParseImage(ref)
			if normalizeRepository(got.Repository) != repo {
				continue
			}
			present = true
			if !imageMatches(want, got) {
				return false, fmt.Sprintf("runs %s, Bundle has %s", ref, formatImage(want))
			}
		}
	}
	if !present {
		return true, "(image not verified: runs none of the Bundle images)"
	}
	return true, ""
}

func formatImage(img ImageExpectation) string {
	s := img.Repository
	if img.Tag != "" {
		s += ":" + img.Tag
	}
	if img.Digest != "" {
		s += "@" + img.Digest
	}
	return s
}

// SameRevision reports whether two git revisions name the same commit. Either
// may be abbreviated; an abbreviation must be at least 7 characters.
func SameRevision(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func shortRev(rev string) string {
	if rev == "" {
		return "<none>"
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
