// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityAdmissionObjects are the admission objects identity-admission.yaml
// renders for release.
func identityAdmissionObjects(release string) []string {
	var out []string
	for _, p := range []string{"scoped-writes"} {
		out = append(out, "ValidatingAdmissionPolicy/"+release+"-"+p, "ValidatingAdmissionPolicyBinding/"+release+"-"+p)
	}
	return out
}

// TestHelmTemplateScopedWritesPolicy verifies the scoped-writes
// ValidatingAdmissionPolicy: always rendered, matching UPDATE on pipelines
// and policygates, exempting the controller and the Graph ServiceAccount,
// checking the virtual subresources with the authorizer, and bound to the
// watched namespace only in namespace mode.
func TestHelmTemplateScopedWritesPolicy(t *testing.T) {
	find := func(docs []map[string]interface{}, kind string) map[string]interface{} {
		for _, d := range docs {
			if d["kind"] == kind && dig(d, "metadata", "name") == "kardinal-promoter-scoped-writes" {
				return d
			}
		}
		return nil
	}
	docs := renderChart(t, "kardinal-promoter")
	vap := find(docs, "ValidatingAdmissionPolicy")
	require.NotNil(t, vap)
	rule := dig(vap, "spec", "matchConstraints", "resourceRules").([]interface{})[0]
	assert.Equal(t, []interface{}{"UPDATE"}, dig(rule, "operations"))
	assert.Equal(t, []interface{}{"pipelines", "policygates"}, dig(rule, "resources"))
	vars := map[string]string{}
	for _, v := range dig(vap, "spec", "variables").([]interface{}) {
		vars[dig(v, "name").(string)] = dig(v, "expression").(string)
	}
	assert.Contains(t, vars["exempt"], `"system:serviceaccount:default:kardinal-promoter"`)
	assert.Contains(t, vars["exempt"], `'kardinal-graph'`)
	assert.Contains(t, vars["limited"], `subresource('edit')`)
	assert.Contains(t, vars["limited"], `'pause' : 'override'`)
	binding := find(docs, "ValidatingAdmissionPolicyBinding")
	require.NotNil(t, binding)
	assert.Equal(t, []interface{}{"Deny"}, dig(binding, "spec", "validationActions"))
	assert.Nil(t, dig(binding, "spec", "matchResources"))

	nsBinding := find(renderChart(t, "kardinal-promoter", "--namespace", "team-a", "--set", "controller.watchNamespace=team-a"),
		"ValidatingAdmissionPolicyBinding")
	assert.Equal(t, "team-a", dig(nsBinding, "spec", "matchResources", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name"))
}
