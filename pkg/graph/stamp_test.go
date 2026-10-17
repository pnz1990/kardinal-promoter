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

// #1360: every gate instance, of every collection, is stamped with the
// generation of its template and the digest of what it copied, in the Graph
// kro renders and in the GateInstances mirror. The digest follows the copied
// fields only.
func TestBuild_GateInstancesStampTheirTemplate(t *testing.T) {
	plain := makePolicyGate("soak", "default", "prod", "upstream.test.soakMinutes >= 30")
	plain.Generation, plain.UID = 7, "uid-soak"
	approval := makePolicyGate("sign-off", "default", "prod", "true")
	approval.Generation, approval.UID = 3, "uid-sign-off"
	approval.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline:    makeLinearPipeline("app", "test", "prod"),
		Bundle:      makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{plain, approval},
	})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)

	want := map[string]map[string]string{
		"soak": {graph.AnnotationGateTemplateGeneration: "7", graph.AnnotationGateTemplateDigest: graph.TemplateDigest(plain),
			graph.AnnotationGateTemplateUID: "uid-soak"},
		"sign-off": {graph.AnnotationGateTemplateGeneration: "3", graph.AnnotationGateTemplateDigest: graph.TemplateDigest(approval),
			graph.AnnotationGateTemplateUID: "uid-sign-off"},
	}
	rendered := gateInstances(t, res.Graph)["prod"]
	require.Len(t, rendered, 2)
	for _, inst := range rendered {
		meta := inst["metadata"].(map[string]interface{})
		template := meta["labels"].(map[string]interface{})["kardinal.io/gate-template"].(string)
		got := map[string]string{}
		for k, v := range meta["annotations"].(map[string]interface{}) {
			got[k] = v.(string)
		}
		assert.Equal(t, want[template], got, "rendered instance of %s", template)
	}
	require.Len(t, res.GateInstances, 2)
	for _, inst := range res.GateInstances {
		assert.Equal(t, want[inst.Labels["kardinal.io/gate-template"]], inst.Annotations, "mirror of %s", inst.Name)
	}

	assert.NotEqual(t, graph.TemplateDigest(plain), graph.TemplateDigest(approval))
	edited := plain
	edited.Spec.Expression = "true"
	assert.NotEqual(t, graph.TemplateDigest(plain), graph.TemplateDigest(edited), "an edit to what is copied")
	edited = plain
	edited.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{Reason: "x"}}
	edited.Labels = map[string]string{"team": "a"}
	assert.Equal(t, graph.TemplateDigest(plain), graph.TemplateDigest(edited), "not an edit to what is not")
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, graph.TemplateDigest(plain))
}
