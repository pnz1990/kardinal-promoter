// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestBuild_FrozenDigestOnlyForKeptTemplates (#2286, QA-2 on #2381): the
// digest a template's instances carry can be set only through
// BuildInput.FrozenDigests, for the templates the translator rebuilt from
// the version's own Graph. A person who writes the
// kardinal.io/frozen-template-digest annotation on a template gets the
// template's own digest: the annotation is dropped, and the gate passed in
// is not changed.
//
// Covers GATE-POLNS-07.
func TestBuild_FrozenDigestOnlyForKeptTemplates(t *testing.T) {
	const annotation = "kardinal.io/frozen-template-digest"
	forged := makePolicyGate("forged", "default", "prod", "true")
	forged.Annotations = map[string]string{annotation: "sha256:forged", "team": "a"}
	kept := makePolicyGate("kept", "team-b", "prod", "true")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: makeLinearPipeline("app", "test", "prod"),
		Bundle: makeBundle("app-v1", "app"), PolicyGates: []kardinalv1alpha1.PolicyGate{forged, kept},
		FrozenDigests: map[string]string{"team-b/kept": "sha256:started"}})
	require.NoError(t, err)
	stamps := graph.TemplateStamps(res.Graph)
	assert.Equal(t, graph.TemplateDigest(forged), stamps["default/forged"].Digest, "a hand-written digest is not honored")
	assert.Equal(t, "sha256:started", stamps["team-b/kept"].Digest, "a kept template keeps the digest it had")
	assert.Equal(t, map[string]string{annotation: "sha256:forged", "team": "a"}, forged.Annotations, "the gate passed in is unchanged")
}
