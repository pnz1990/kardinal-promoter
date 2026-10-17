// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// crTemplate is a per-promotion ServiceNow ChangeRequest in default.
func crTemplate(name string, appliesTo ...string) kardinalv1alpha1.ChangeRequest {
	return kardinalv1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kardinalv1alpha1.ChangeRequestSpec{PerPromotion: true, AppliesTo: appliesTo, Provider: "servicenow", Mode: "open",
			PollInterval: "1m", Timeout: "24h",
			ServiceNow: &kardinalv1alpha1.ServiceNowChangeRequest{InstanceURL: "https://example.service-now.com",
				ChangeRequestAuth: kardinalv1alpha1.ChangeRequestAuth{
					CredentialsSecretRef: kardinalv1alpha1.SecretKeyRef{Name: "snow", Key: "token"}, Scheme: "basic"},
				Template: map[string]string{
					"short_description": "Promote {{ pipeline.name }} {{ bundle.version }} to {{ environment.name }}",
					"description":       "built from ${commit} {{ bundle.nope }}",
				}}},
	}
}

// crNodes returns the ChangeRequest nodes of a Graph by metadata.name.
func crNodes(g *graph.Graph) map[string]graph.GraphNode {
	out := map[string]graph.GraphNode{}
	for _, n := range g.Spec.Nodes {
		if n.Template["kind"] == "ChangeRequest" && n.ForEach == nil {
			out[n.Template["metadata"].(map[string]interface{})["name"].(string)] = n
		}
	}
	return out
}

func TestExprReadsChangeRequest(t *testing.T) {
	for expr, want := range map[string]bool{
		`changeRequests["prod-change"].state == "approved"`:               true,
		`changeRequests['prod-change'].state == "approved"`:               true,
		`changeRequests[ "prod-change" ].stale`:                           true,
		`changeRequests.prodChange.state == "approved"`:                   false, // another name
		`changeRequests.prod.state == "approved"`:                         false, // a prefix of prod-change is not it
		`metrics["prod-change"].result == "Pass"`:                         false, // the quoted name elsewhere: no ticket
		`"prod-change" in changeRequests`:                                 false,
		`changeRequests["prod-change2"].state == "approved"`:              false,
		`!changeRequests["prod-change"].timedOut || true`:                 true,
		`bundle.version == "1" && changeRequests["prod-change"].verified`: true,
	} {
		assert.Equal(t, want, graph.ExprReadsChangeRequest(expr, "prod-change"), expr)
	}
	assert.True(t, graph.ExprReadsChangeRequest(`changeRequests.cab.state == "approved"`, "cab"))
	assert.False(t, graph.ExprReadsChangeRequest(`changeRequests.cabinet.state == "approved"`, "cab"))
	assert.True(t, graph.ExprReadsChangeRequest(`changeRequests.cab`, "cab"), "at the end")
}

func TestRenderChangeRequestText(t *testing.T) {
	vars := map[string]string{"bundle.version": `1.2"3` + "\n\x00x", "pipeline.name": "app", "bundle.commitSHA": "",
		"environment.name": strings.Repeat("é", 200)}
	assert.Equal(t, `app 1.2"3x`, graph.RenderChangeRequestText("{{ pipeline.name }} {{bundle.version}}", vars),
		"any value is put into a ticket field, control characters dropped")
	assert.Equal(t, "c=", graph.RenderChangeRequestText("c={{ bundle.commitSHA }}", vars), "an empty value is empty")
	assert.Equal(t, "{{ bundle.nope }}", graph.RenderChangeRequestText("{{ bundle.nope }}", vars))
	long := graph.RenderChangeRequestText("{{ environment.name }}", vars)
	assert.LessOrEqual(t, len(long), graph.MaxChangeRequestValue)
	assert.Equal(t, strings.Repeat("é", 128), long, "cut at a character boundary")
}

// TestChangeRequestApplies: appliesTo unset is every environment; a fleet
// target counts as its fleet.
func TestChangeRequestApplies(t *testing.T) {
	all, prod := crTemplate("c"), crTemplate("c", "prod", "eu")
	assert.True(t, graph.ChangeRequestApplies(&all, "test", ""))
	assert.True(t, graph.ChangeRequestApplies(&prod, "prod", ""))
	assert.False(t, graph.ChangeRequestApplies(&prod, "uat", ""))
	assert.True(t, graph.ChangeRequestApplies(&prod, "eu-west-1", "eu"))
	assert.False(t, graph.ChangeRequestApplies(&prod, "us-east-1", "us"))
}

// TestBuilder_PerPromotionChangeRequest (spec 23 §4): an instance per
// environment whose gate reads the template and that it applies to; named
// and labelled as a MetricCheck instance plus the Bundle UID; the ticket
// template rendered; provider held until the upstreams are Verified;
// suspend following the Bundle phase. Templates no gate reads, that do not
// apply, of another namespace, or read only by an org gate get none.
func TestBuilder_PerPromotionChangeRequest(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	b := makeBundle("app-v1", "app")
	b.UID = types.UID("uid-1")
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("cab", "default", "prod", `changeRequests["prod-change"].state == "approved"`),
		makePolicyGate("cab-uat", "default", "uat", `changeRequests["prod-change"].state == "approved"`), // not applied to uat
		makePolicyGate("all", "default", "test", `changeRequests.everywhere.state == "approved"`),
		makePolicyGate("org", "platform-policies", "uat", `changeRequests["org-only"].state == "approved"`),
		makePolicyGate("metric", "default", "prod", `metrics["unread"].result == "Pass"`),
	}
	other := crTemplate("prod-change", "prod")
	other.Namespace = "elsewhere"
	notTemplate := crTemplate("everywhere")
	notTemplate.Name, notTemplate.Spec.PerPromotion = "everywhere-hand", false
	crs := []kardinalv1alpha1.ChangeRequest{crTemplate("prod-change", "prod"), crTemplate("everywhere"),
		crTemplate("org-only"), crTemplate("unread"), other, notTemplate}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, PolicyGates: gates, ChangeRequests: crs,
		PolicyNamespaces: []string{"platform-policies"}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)

	nodes := crNodes(res.Graph)
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"everywhere-test--app-v1", "prod-change-prod--app-v1"}, names)

	n := nodes["prod-change-prod--app-v1"]
	assert.Equal(t, "creq0prodChange0prod00appV1", n.ID)
	meta := n.Template["metadata"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod",
		graph.LabelBundleUID: "uid-1", graph.LabelChangeRequestTemplate: "prod-change",
	}, meta["labels"])
	spec := n.Template["spec"].(map[string]interface{})
	assert.NotContains(t, spec, "perPromotion")
	assert.NotContains(t, spec, "appliesTo")
	assert.Equal(t, `${!(bundle.status.phase in ["Available", "Promoting"])}`, spec["suspend"])
	assert.Equal(t, `${["servicenow"].filter(x_, uat.status.state == "Verified")[0]}`, spec["provider"],
		"the instance, and its ticket, only once uat is Verified")
	snow := spec["servicenow"].(map[string]interface{})
	tmpl := snow["template"].(map[string]interface{})
	assert.Equal(t, "Promote app v1 to prod", tmpl["short_description"])
	assert.Equal(t, `${"built from ${commit} {{ bundle.nope }}"}`, tmpl["description"], "a ${ is a CEL literal for kro")
	assert.Equal(t, "snow", snow["credentialsSecretRef"].(map[string]interface{})["name"])

	root := nodes["everywhere-test--app-v1"].Template["spec"].(map[string]interface{})
	assert.Equal(t, "servicenow", root["provider"], "a root environment's instance is not held")
	require.NoError(t, graph.ValidateNodeIDs(res.Graph.Spec.Nodes))
}

// TestCompact_ChangeRequestAdmission: the compact shape admits a
// ChangeRequest instance as it admits a MetricCheck one, with the same
// rendered spec as the node shape (without the hold) and the Bundle UID
// label.
func TestCompact_ChangeRequestAdmission(t *testing.T) {
	p := compactPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"test"}},
	)
	gates := []kardinalv1alpha1.PolicyGate{makePolicyGate("cab", "default", "prod", `changeRequests["prod-change"].state == "approved"`),
		makePolicyGate("cab-all", "default", "test", `changeRequests.everywhere.state == "approved"`)}
	b := makeBundle("app-x7k2m", "app")
	b.UID = types.UID("uid-7")
	crs := []kardinalv1alpha1.ChangeRequest{crTemplate("prod-change", "prod"), crTemplate("everywhere")}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, PolicyGates: gates, ChangeRequests: crs})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	sim := newCompactSim(t, res.Graph)
	admitted := func() []string {
		vars := sim.vars()
		vars[graph.NodePromotionState] = sim.def(graph.NodePromotionState, vars)
		var envs []string
		for _, m := range sim.def(graph.NodePromotionChangeRequests, vars)["items"].([]interface{}) {
			envs = append(envs, m.(map[string]interface{})["environment"].(string))
		}
		return envs
	}
	assert.Equal(t, []string{"test"}, admitted(), "prod waits for test; the root has no upstreams")
	sim.steps["test"] = "Verified"
	assert.ElementsMatch(t, []string{"prod", "test"}, admitted())

	var coll graph.GraphNode
	var items []interface{}
	for _, n := range res.Graph.Spec.Nodes {
		switch n.ID {
		case graph.NodeChangeRequests:
			coll = n
		case graph.NodeChangeRequestData:
			items = n.Def["items"].([]interface{})
		}
	}
	require.NotNil(t, coll.Template)
	assert.Equal(t, "ChangeRequest", coll.Template["kind"])
	labels := coll.Template["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
	assert.Equal(t, "uid-7", labels[graph.LabelBundleUID])
	assert.Equal(t, "${ChangeReq.template}", labels[graph.LabelChangeRequestTemplate])
	require.Len(t, items, 2)
	var spec map[string]interface{}
	for _, it := range items {
		if it.(map[string]interface{})["environment"] == "prod" {
			spec = it.(map[string]interface{})["spec"].(map[string]interface{})
		}
	}
	require.NotNil(t, spec)
	assert.Equal(t, "servicenow", spec["provider"], "the compact shape holds by admission, not by the field")
	assert.Equal(t, "Promote app v1 to prod",
		spec["servicenow"].(map[string]interface{})["template"].(map[string]interface{})["short_description"])

	nodesP := p.DeepCopy()
	nodesP.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeNodes}
	nodesRes, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: nodesP, Bundle: b, PolicyGates: gates, ChangeRequests: crs})
	require.NoError(t, err)
	assert.Equal(t, graph.ObjectCount(nodesRes.Graph), graph.ObjectCount(res.Graph),
		"the size guard counts the compact instances as the node shape's")
}
