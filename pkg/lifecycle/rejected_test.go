// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestRejectedArtifacts: a Bundle carries a rejected artifact when it shares
// an image or the config commit of a rejected Bundle of the same pipeline. A
// rejected image with a digest matches by digest only, so a moving tag
// (r/a:latest@bad rejected) does not block a fixed push of the same tag
// (r/a:latest@fixed); a rejected image without a digest matches by tag.
// Another pipeline's rejections and other repositories do not count.
//
// Covers BUNDLE-REJECT-06.
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
		rejected(withImage("bad-latest", "app", v1alpha1.ImageRef{Repository: "r/l", Tag: "latest", Digest: "sha256:bad"})),
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
		{name: "moving tag, the rejected digest", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/l", Tag: "latest", Digest: "sha256:bad"}), want: "bad-latest"},
		{name: "moving tag, a fixed digest", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/l", Tag: "latest", Digest: "sha256:fixed"})},
		{name: "moving tag without a digest", b: withImage("x", "app", v1alpha1.ImageRef{Repository: "r/l", Tag: "latest"})},
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
	// Two images (QA #1489): the recorded set (status.rejectedArtifacts)
	// rejects only the app image that changed, not the sidecar it carried
	// unchanged; without a recorded set every image of the Bundle counts.
	app2 := v1alpha1.ImageRef{Repository: "r/app", Tag: "2", Digest: "sha256:a2"}
	side := v1alpha1.ImageRef{Repository: "r/side", Tag: "1", Digest: "sha256:s1"}
	two := rejected(bundle("two", "app", "", 0))
	two.Spec.Images = []v1alpha1.ImageRef{app2, side}
	recorded := two.DeepCopy()
	recorded.Name = "two-recorded"
	recorded.Status.RejectedArtifacts = &v1alpha1.RejectedArtifactSet{Images: []v1alpha1.ImageRef{app2}}
	withBoth := func(imgs ...v1alpha1.ImageRef) *v1alpha1.Bundle {
		b := bundle("x", "app", "", 0)
		b.Spec.Images = imgs
		return b
	}
	app1 := v1alpha1.ImageRef{Repository: "r/app", Tag: "1", Digest: "sha256:a1"}
	set := lifecycle.RejectedArtifactsOf([]v1alpha1.Bundle{*recorded}, "app")
	_, ok := set.Carries(withBoth(app1, side))
	assert.False(t, ok, "the unchanged sidecar is not rejected")
	name, ok := set.Carries(withBoth(app2, v1alpha1.ImageRef{Repository: "r/side", Tag: "2", Digest: "sha256:s2"}))
	assert.True(t, ok, "the changed app image is")
	assert.Equal(t, "two-recorded", name)
	_, ok = lifecycle.RejectedArtifactsOf([]v1alpha1.Bundle{*two}, "app").Carries(withBoth(app1, side))
	assert.True(t, ok, "an unrecorded rejection covers every image")
	_, ok = lifecycle.RecordedRejectedArtifactsOf([]v1alpha1.Bundle{*two}, "app").Carries(withBoth(app1, side))
	assert.False(t, ok, "a carrier is marked only from a recorded set")

	var nilSet *lifecycle.RejectedArtifacts
	_, ok = nilSet.Carries(withImage("x", "app", v1alpha1.ImageRef{Repository: "r/a", Digest: "sha256:aa"}))
	assert.False(t, ok, "a nil set carries nothing")
}

// TestRejectedSetOf (QA #1489): rejecting a Bundle rejects the artifacts
// that differ from the Bundle Verified before it in the environments it
// reached; with no predecessor there, all of them.
//
// Covers BUNDLE-REJECT-09.
func TestRejectedSetOf(t *testing.T) {
	img := func(repo, tag string) v1alpha1.ImageRef { return v1alpha1.ImageRef{Repository: repo, Tag: tag} }
	mk := func(name string, minute int, envs [][2]string, imgs ...v1alpha1.ImageRef) v1alpha1.Bundle {
		b := bundle(name, "app", "", minute)
		b.Spec.Images = imgs
		for _, e := range envs {
			b.Status.Environments = append(b.Status.Environments, v1alpha1.EnvironmentStatus{Name: e[0], Phase: e[1]})
		}
		return *b
	}
	v0 := mk("v0", -10, [][2]string{{"test", "Verified"}}, img("app", "0"), img("side", "s0"))
	v1 := mk("v1", 0, [][2]string{{"test", "Verified"}, {"prod", "Verified"}}, img("app", "1"), img("side", "s1"))
	v2 := mk("v2", 10, [][2]string{{"test", "Verified"}, {"prod", "WaitingForMerge"}}, img("app", "2"), img("side", "s1"))
	v3 := mk("v3", 20, [][2]string{{"test", "Verified"}, {"prod", "HealthChecking"}}, img("app", "3"), img("side", "s0"))
	// Two environments with different predecessors: test has v2 (app 2,
	// side s2), prod still v1 (app 1, side s1). v4 (app 3, side s2) was
	// merged to prod and then failed its health check: the change was
	// deployed there, so prod is compared too and side s2, new in prod, is
	// rejected with app 3. Failed before the merge, prod does not count.
	w1 := mk("w1", 0, [][2]string{{"test", "Verified"}, {"prod", "Verified"}}, img("app", "1"), img("side", "s1"))
	w2 := mk("w2", 10, [][2]string{{"test", "Verified"}, {"prod", "WaitingForMerge"}}, img("app", "2"), img("side", "s2"))
	w4 := mk("w4", 20, [][2]string{{"test", "Verified"}, {"prod", "Failed"}}, img("app", "3"), img("side", "s2"))
	healthFailed := *step("w4", "app", "prod", "Failed", 25)
	expiry := metav1.NewTime(t0.Add(26 * time.Minute))
	healthFailed.Status.HealthCheckExpiry = &expiry
	mergeFailed := *step("w4", "app", "prod", "Failed", 25)
	cases := []struct {
		name     string
		b        v1alpha1.Bundle
		bundles  []v1alpha1.Bundle
		want     []v1alpha1.ImageRef
		compared []string
		steps    []v1alpha1.PromotionStep
	}{
		{name: "only the changed image", b: v2, bundles: []v1alpha1.Bundle{v0, v1, v2},
			want: []v1alpha1.ImageRef{img("app", "2")}, compared: []string{"test=v1"}},
		{name: "no predecessor: every image", b: v2, bundles: []v1alpha1.Bundle{v2},
			want: []v1alpha1.ImageRef{img("app", "2"), img("side", "s1")}},
		{name: "a newer Bundle is not a predecessor", b: v1, bundles: []v1alpha1.Bundle{v1, v2},
			want: []v1alpha1.ImageRef{img("app", "1"), img("side", "s1")}},
		{name: "a failed health check deployed the change: prod is compared", b: w4, bundles: []v1alpha1.Bundle{w1, w2, w4},
			steps: []v1alpha1.PromotionStep{healthFailed},
			want:  []v1alpha1.ImageRef{img("app", "3"), img("side", "s2")}, compared: []string{"prod=w1", "test=w2"}},
		{name: "failed before the merge: prod is not compared", b: w4, bundles: []v1alpha1.Bundle{w1, w2, w4},
			steps: []v1alpha1.PromotionStep{mergeFailed},
			want:  []v1alpha1.ImageRef{img("app", "3")}, compared: []string{"test=w2"}},
		{name: "every reached environment is compared", b: v3, bundles: []v1alpha1.Bundle{v0, v1, v3},
			want: []v1alpha1.ImageRef{img("app", "3"), img("side", "s0")}, compared: []string{"prod=v1", "test=v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := lifecycle.RejectedSetOf(&tc.b, tc.bundles, tc.steps)
			assert.Equal(t, tc.want, set.Images)
			assert.Equal(t, tc.compared, set.ComparedWith)
		})
	}
}

// TestRejectedLiveRetired (QA #1489, #1492): a Verified Bundle rejected after
// its Graph was retired keeps only status.retiredSteps; with them
// (AddRetiredSteps) it is still live and current, so the views keep showing
// the rejected change with the roll-back hint.
func TestRejectedLiveRetired(t *testing.T) {
	at := metav1.NewTime(t0)
	old := bundle("v1", "app", "1", 0)
	bad := rejected(bundle("v2", "app", "2", 10))
	bad.Status.RetiredAt = &at
	bad.Status.RetiredSteps = []v1alpha1.RetiredStep{{Name: "v2-prod", Environment: "prod", State: "Verified", CreatedAt: at, VerifiedAt: &at}}
	bundles := []v1alpha1.Bundle{*old, *bad}
	steps := lifecycle.AddRetiredSteps([]v1alpha1.PromotionStep{*step("v1", "app", "prod", "Verified", 1)}, bundles, nil)
	assert.Equal(t, []string{"prod"}, lifecycle.RejectedLiveEnvs(bad, steps))
	cur := lifecycle.CurrentBundle(bundles, steps)
	require.NotNil(t, cur)
	assert.Equal(t, "v2", cur.Name, "the live rejected change stays current")
	assert.Empty(t, lifecycle.RejectedLiveEnvs(bad, nil), "without the retired steps it would be hidden")
}
