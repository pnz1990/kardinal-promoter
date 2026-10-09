// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestRejectedArtifacts: a Bundle carries a rejected artifact when it shares
// an image (same repository and digest, or same tag) or the config commit of
// a rejected Bundle of the same pipeline; another pipeline's rejections and
// other repositories do not count.
func TestRejectedArtifacts(t *testing.T) {
	withImage := func(name, pipe string, img v1alpha1.ImageRef) *v1alpha1.Bundle {
		b := bundle(name, pipe, "", 0)
		b.Spec.Images = []v1alpha1.ImageRef{img}
		return b
	}
	cfg := func(name, sha string) *v1alpha1.Bundle {
		b := bundle(name, "app", "", 0)
		b.Spec.Type = "config"
		b.Spec.ConfigRef = &v1alpha1.ConfigRef{CommitSHA: sha}
		return b
	}
	c := newClient(t,
		rejected(withImage("bad-digest", "app", v1alpha1.ImageRef{Repository: "r/a", Digest: "sha256:aa"})),
		rejected(withImage("bad-tag", "app", v1alpha1.ImageRef{Repository: "r/b", Tag: "2"})),
		rejected(cfg("bad-config", "c0ffee")),
		rejected(withImage("other-pipe", "other", v1alpha1.ImageRef{Repository: "r/c", Tag: "1"})),
	)
	rej, err := lifecycle.LoadRejectedArtifacts(context.Background(), c, ns, "app")
	require.NoError(t, err)

	cases := []struct {
		name string
		b    *v1alpha1.Bundle
		want string
	}{
		{name: "same digest, other tag", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/a", Tag: "9", Digest: "sha256:aa"}), want: "bad-digest"},
		{name: "same tag", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/b", Tag: "2", Digest: "sha256:bb"}), want: "bad-tag"},
		{name: "same config commit", b: cfg("x", "c0ffee"), want: "bad-config"},
		{name: "other repository", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/z", Tag: "2"})},
		{name: "other digest", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/a", Digest: "sha256:ab"})},
		{name: "another pipeline's rejection", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/c", Tag: "1"})},
		{name: "itself rejected", b: rejected(withImage("self", "app", v1alpha1.ImageRef{Repository: "r/q", Tag: "1"})), want: "self"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, ok := rej.Carries(tc.b)
			assert.Equal(t, tc.want != "", ok)
			assert.Equal(t, tc.want, name)
		})
	}
	var nilSet *lifecycle.RejectedArtifacts
	_, ok := nilSet.Carries(withImage("x", "app", v1alpha1.ImageRef{Repository: "r/a", Digest: "sha256:aa"}))
	assert.False(t, ok, "a nil set carries nothing")
}
