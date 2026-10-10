// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

var ivDigest = "sha256:" + strings.Repeat("b", 64)

func ivPipeline(images ...string) *kardinalv1alpha1.Pipeline {
	p := makeLinearPipeline("app", "test", "prod")
	p.Spec.ImageVerification = &kardinalv1alpha1.ImageVerificationPolicy{
		Images: images,
		Authorities: []kardinalv1alpha1.SignatureAuthority{{Name: "release",
			Key: &kardinalv1alpha1.KeyAuthority{SecretRef: kardinalv1alpha1.SecretKeyName{Name: "cosign", Key: "cosign.pub"}}}},
	}
	return p
}

func ivBundle(images ...kardinalv1alpha1.ImageRef) *kardinalv1alpha1.Bundle {
	b := makeBundle("app-v1", "app")
	b.Spec.Images = images
	return b
}

// TestBuilder_ImageVerificationNode: a Pipeline with an image policy gets
// one ImageVerification node with the selected images, by digest, and the
// policy; only the root step waits for it, through the mirror.
func TestBuilder_ImageVerificationNode(t *testing.T) {
	b := ivBundle(
		kardinalv1alpha1.ImageRef{Repository: "ghcr.io/org/app", Digest: ivDigest},
		kardinalv1alpha1.ImageRef{Repository: "docker.io/library/redis", Tag: "7"}, // not selected
	)
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: ivPipeline("ghcr.io/org/*"), Bundle: b})
	require.NoError(t, err)
	g := res.Graph
	node := hookNode(t, g, "imageVerify").Template
	assert.Equal(t, "ImageVerification", node["kind"])
	md := node["metadata"].(map[string]interface{})
	ivName := md["name"].(string)
	assert.True(t, strings.HasPrefix(ivName, "app-app-v1-verify-"), "no gating, a spec hash: %s", ivName)
	spec := node["spec"].(map[string]interface{})
	assert.Equal(t, []interface{}{map[string]interface{}{"repository": "ghcr.io/org/app", "digest": ivDigest}}, spec["images"])
	policy := spec["policy"].(map[string]interface{})
	assert.NotContains(t, policy, "images", "the selector is applied, not copied")
	assert.NotEmpty(t, policy["authorities"])

	test := hookNode(t, g, "test").Template["spec"].(map[string]interface{})
	assert.Equal(t, ivName, test["imageVerification"], "the root step waits")
	prod := hookNode(t, g, "prod").Template["spec"].(map[string]interface{})
	assert.NotContains(t, prod, "imageVerification", "downstream steps need nothing")
	live := hookNode(t, g, "live0test").Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"name":    `${imageVerify.metadata.name}`,
		"phase":   `${imageVerify.?status.?phase.orValue("Pending")}`,
		"message": `${imageVerify.?status.?message.orValue("")}`,
		"images":  `${imageVerify.spec.?images.orValue([]).map(i, i.repository + "@" + i.digest)}`,
	}, live["imageVerification"])
	out, err := celEval(t, live["imageVerification"].(map[string]interface{})["images"].(string), map[string]interface{}{
		"imageVerify": map[string]interface{}{"spec": map[string]interface{}{"images": spec["images"]}}})
	require.NoError(t, err)
	assert.Equal(t, []interface{}{"ghcr.io/org/app@" + ivDigest}, out)
	assert.False(t, hasNode(g, "live0prod"))
}

// TestBuilder_ImageVerificationNeedsDigests: a selected image without a
// digest fails the Build (verify-then-promote by tag is a TOCTOU hole).
func TestBuilder_ImageVerificationNeedsDigests(t *testing.T) {
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: ivPipeline(),
		Bundle: ivBundle(kardinalv1alpha1.ImageRef{Repository: "ghcr.io/org/app", Tag: "1.2.3"})})
	require.Error(t, err)
	assert.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), "ghcr.io/org/app:1.2.3 is not pinned by digest")

	// Not selected: no digest needed, and nothing to verify means no node.
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: ivPipeline("ghcr.io/other/*"),
		Bundle: ivBundle(kardinalv1alpha1.ImageRef{Repository: "ghcr.io/org/app", Tag: "1.2.3"})})
	require.NoError(t, err)
	assert.False(t, hasNode(res.Graph, "imageVerify"))
}

// TestBuilder_ImageVerificationCommit: commits.requireSigned adds the
// config Bundle's commit; the repository defaults to the Pipeline's.
func TestBuilder_ImageVerificationCommit(t *testing.T) {
	p := makeLinearPipeline("app", "prod")
	p.Spec.Git.URL = "https://github.com/org/gitops"
	p.Spec.ImageVerification = &kardinalv1alpha1.ImageVerificationPolicy{
		Commits: &kardinalv1alpha1.CommitSignaturePolicy{RequireSigned: true}}
	b := makeBundle("cfg-1", "app")
	b.Spec.Type, b.Spec.Images = "config", nil
	sha := strings.Repeat("abc1", 10)
	b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{CommitSHA: sha}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
	require.NoError(t, err)
	spec := hookNode(t, res.Graph, "imageVerify").Template["spec"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"repo": "https://github.com/org/gitops", "sha": sha}, spec["commit"])
	plainName := hookNode(t, res.Graph, "imageVerify").Template["metadata"].(map[string]interface{})["name"]

	// The Pipeline's spec.git.providerRef: the commit is checked with that
	// provider, and it is in the spec, so the name (hash) changes with it
	// (#1618).
	id := &kardinalv1alpha1.ScmProviderIdentity{Kind: kardinalv1alpha1.KindScmProvider, Name: "team", UID: "uid-1"}
	res, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, ScmProvider: id})
	require.NoError(t, err)
	node := hookNode(t, res.Graph, "imageVerify").Template
	assert.Equal(t, map[string]interface{}{"repo": "https://github.com/org/gitops", "sha": sha,
		"scmProvider": map[string]interface{}{"kind": "ScmProvider", "name": "team", "uid": "uid-1"}},
		node["spec"].(map[string]interface{})["commit"])
	assert.NotEqual(t, plainName, node["metadata"].(map[string]interface{})["name"], "another provider, another ImageVerification")

	// A short SHA is refused: a signed commit is checked by its full SHA
	// (regression, QA #1521).
	b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{CommitSHA: "abc123"}
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `configRef.commitSHA "abc123" is not a full 40- or 64-character commit SHA`)
}

// TestBuilder_ImageVerificationNormalizesReferences: one repository written
// different ways is selected and verified as one (regression, QA #1521:
// "Ghcr.io/org/app", "ghcr.io:443/org/app" and "nginx" escaped a policy
// written for "ghcr.io/org/*" or "docker.io/library/*"); a reference that
// does not parse fails the Build.
func TestBuilder_ImageVerificationNormalizesReferences(t *testing.T) {
	cases := []struct {
		pattern, repo, want string
	}{
		{"ghcr.io/org/*", "Ghcr.IO/org/app", "ghcr.io/org/app"},
		{"ghcr.io/org/*", "ghcr.io:443/org/app", "ghcr.io/org/app"},
		{"ghcr.io/org/*", "https://ghcr.io/org/app", "ghcr.io/org/app"},
		{"docker.io/library/*", "nginx", "docker.io/library/nginx"},
		{"nginx", "index.docker.io/library/nginx", "docker.io/library/nginx"},
		{"docker.io/myorg/*", "registry-1.docker.io/myorg/app", "docker.io/myorg/app"},
		{"myorg/*", "docker.io/myorg/app", "docker.io/myorg/app"},
		{"localhost:5000/*", "LOCALHOST:5000/app", "localhost:5000/app"},
	}
	for _, tc := range cases {
		t.Run(tc.repo, func(t *testing.T) {
			res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: ivPipeline(tc.pattern),
				Bundle: ivBundle(kardinalv1alpha1.ImageRef{Repository: tc.repo, Digest: ivDigest})})
			require.NoError(t, err)
			require.True(t, hasNode(res.Graph, "imageVerify"), "%s selected by %s", tc.repo, tc.pattern)
			spec := hookNode(t, res.Graph, "imageVerify").Template["spec"].(map[string]interface{})
			assert.Equal(t, []interface{}{map[string]interface{}{"repository": tc.want, "digest": ivDigest}}, spec["images"])
		})
	}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: ivPipeline(),
		Bundle: ivBundle(kardinalv1alpha1.ImageRef{Repository: "ghcr.io/Org/App", Digest: ivDigest})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a valid reference")
}

// TestBuilder_ImageVerificationRenamedOnPolicyChange: the ImageVerification
// name carries a hash of its spec, so a policy change creates a new one (its
// spec is immutable) and the steps that did not consume the old verdict wait
// for the new one (regression, QA #1521).
func TestBuilder_ImageVerificationRenamedOnPolicyChange(t *testing.T) {
	b := ivBundle(kardinalv1alpha1.ImageRef{Repository: "ghcr.io/org/app", Digest: ivDigest})
	name := func(p *kardinalv1alpha1.Pipeline) string {
		res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
		require.NoError(t, err)
		return hookNode(t, res.Graph, "imageVerify").Template["metadata"].(map[string]interface{})["name"].(string)
	}
	before := name(ivPipeline())
	assert.Equal(t, before, name(ivPipeline()), "stable")
	p := ivPipeline()
	p.Spec.ImageVerification.Authorities[0].Key.SecretRef.Name = "cosign-2026"
	assert.NotEqual(t, before, name(p))
}

// TestBuilder_ImageVerificationHoldsPreHooks: a root environment's pre
// hooks also wait for the verification (no migration for an unverified
// image).
func TestBuilder_ImageVerificationHoldsPreHooks(t *testing.T) {
	p := ivPipeline()
	p.Spec.Environments[0].Hooks = []kardinalv1alpha1.HookSpec{hook("migrate", "pre", hookJob)}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p,
		Bundle: ivBundle(kardinalv1alpha1.ImageRef{Repository: "ghcr.io/org/app", Digest: ivDigest})})
	require.NoError(t, err)
	expr := nameExpr(t, res.Graph, "hook0pre0test0migrate")
	vars := func(phase string) map[string]interface{} {
		return map[string]interface{}{
			"bundle":      map[string]interface{}{"status": map[string]interface{}{"phase": "Promoting"}},
			"imageVerify": map[string]interface{}{"status": map[string]interface{}{"phase": phase}},
		}
	}
	_, err = celEval(t, expr, vars("Pending"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index out of bounds")
	out, err := celEval(t, expr, vars("Verified"))
	require.NoError(t, err)
	assert.Contains(t, out, "pre-migrate")
}

func TestImageVerificationName(t *testing.T) {
	assert.Equal(t, "app-v1-verify-12345678", graph.ImageVerificationName("app", "v1", "12345678"))
	assert.NotEqual(t, graph.ImageVerificationName("app", "V1", "12345678"), graph.ImageVerificationName("app", "v1", "12345678"))
	assert.NotEqual(t, graph.ImageVerificationName("app", "v1", "12345678"), graph.ImageVerificationName("app", "v1", "87654321"))
}
