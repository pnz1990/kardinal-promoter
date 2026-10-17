// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// QA-3 on #2187: deciding whether a version is a rollback, and what a
// rollback left, matches image refs per repository: by digest when both
// name one, else by tag, with the repository normalized. A held rollback
// still needs the exact refs (a tag alone can have moved).
//
// Covers RB-AUTO-FORGED-08.
func TestLooseImage(t *testing.T) {
	ref := func(repo, tag, digest string) v1alpha1.ImageRef {
		return v1alpha1.ImageRef{Repository: repo, Tag: tag, Digest: digest}
	}
	const a1, a2 = "sha256:a1", "sha256:a2"
	for _, tc := range []struct {
		name string
		a, b v1alpha1.ImageRef
		want bool
	}{
		{"tag only and the same tag pinned to a digest", ref("ghcr.io/x/app", "1", ""), ref("ghcr.io/x/app", "1", a1), true},
		{"digest only and tag with that digest", ref("ghcr.io/x/app", "", a1), ref("ghcr.io/x/app", "1", a1), true},
		{"another tag at the same digest", ref("ghcr.io/x/app", "latest", a1), ref("ghcr.io/x/app", "1", a1), true},
		{"the same tag at another digest", ref("ghcr.io/x/app", "1", a2), ref("ghcr.io/x/app", "1", a1), false},
		{"tag only and digest only", ref("ghcr.io/x/app", "1", ""), ref("ghcr.io/x/app", "", a1), false},
		{"another tag", ref("ghcr.io/x/app", "2", ""), ref("ghcr.io/x/app", "1", ""), false},
		{"Docker Hub without a host", ref("nginx", "1.27", ""), ref("docker.io/library/nginx", "1.27", ""), true},
		{"Docker Hub's index host", ref("index.docker.io/library/nginx", "1.27", ""), ref("nginx", "1.27", ""), true},
		{"a registry host in capitals", ref("GHCR.io/x/app", "1", ""), ref("ghcr.io/x/app", "1", ""), true},
		{"another repository", ref("ghcr.io/x/other", "1", a1), ref("ghcr.io/x/app", "1", a1), false},
		{"another registry", ref("quay.io/x/app", "1", ""), ref("ghcr.io/x/app", "1", ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, looseImage(tc.a, tc.b))
			assert.Equal(t, tc.want, looseImage(tc.b, tc.a), "symmetric")
			assert.Equal(t, tc.a == tc.b, exactImage(tc.a, tc.b))
		})
	}
}
