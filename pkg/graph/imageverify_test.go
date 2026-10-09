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
	assert.Equal(t, "app-app-v1-verify", md["name"], "no gating: created with the Graph")
	spec := node["spec"].(map[string]interface{})
	assert.Equal(t, []interface{}{map[string]interface{}{"repository": "ghcr.io/org/app", "digest": ivDigest}}, spec["images"])
	policy := spec["policy"].(map[string]interface{})
	assert.NotContains(t, policy, "images", "the selector is applied, not copied")
	assert.NotEmpty(t, policy["authorities"])

	test := hookNode(t, g, "test").Template["spec"].(map[string]interface{})
	assert.Equal(t, "app-app-v1-verify", test["imageVerification"], "the root step waits")
	prod := hookNode(t, g, "prod").Template["spec"].(map[string]interface{})
	assert.NotContains(t, prod, "imageVerification", "downstream steps need nothing")
	live := hookNode(t, g, "live0test").Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"phase":   `${imageVerify.?status.?phase.orValue("Pending")}`,
		"message": `${imageVerify.?status.?message.orValue("")}`,
	}, live["imageVerification"])
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
	b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{CommitSHA: "abc123"}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
	require.NoError(t, err)
	spec := hookNode(t, res.Graph, "imageVerify").Template["spec"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"repo": "https://github.com/org/gitops", "sha": "abc123"}, spec["commit"])
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
	assert.Equal(t, "app-v1-verify", graph.ImageVerificationName("app", "v1"))
	assert.NotEqual(t, graph.ImageVerificationName("app", "V1"), graph.ImageVerificationName("app", "v1"))
}
