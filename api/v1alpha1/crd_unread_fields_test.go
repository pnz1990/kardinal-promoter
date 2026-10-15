// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

// Admission tests for the fields v0.11.0 removed and for the argocd +
// pr-review combination the argocd strategy refuses (#1281).
//
// celAdmission runs the API server's own CEL validator
// (k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel), not the cel-go
// approximation in crd_schema_test.go, so it also covers CRD validation
// ratcheting: with ratcheting (CRDValidationRatcheting, GA on every Kubernetes
// kardinal supports) a rule error is dropped when the schema node that carries
// the rule is unchanged from the stored object.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/model"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/cel/common"
)

// celAdmission returns the messages of the x-kubernetes-validations rules the
// API server would fail for obj. oldObj is nil on create. ratchet mirrors the
// CRDValidationRatcheting feature gate on an update.
func celAdmission(t *testing.T, crds map[string]loadedCRD, obj, oldObj map[string]interface{}, ratchet bool) []string {
	t.Helper()
	kind, _ := obj["kind"].(string)
	c, ok := crds[kind]
	require.True(t, ok, "no CRD for kind %q", kind)
	v := cel.NewValidator(c.structural, true, celconfig.PerCallLimit)
	require.NotNil(t, v, "%s CRD has no CEL rules", kind)
	var opts []cel.Option
	if ratchet && oldObj != nil {
		opts = append(opts, cel.WithRatcheting(common.NewCorrelatedObject(obj, oldObj, &model.Structural{Structural: c.structural})))
	}
	var old interface{}
	if oldObj != nil {
		old = oldObj
	}
	errs, _ := v.Validate(context.Background(), nil, c.structural, obj, old, celconfig.RuntimeCELCostBudget, opts...)
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

func crFromYAML(t *testing.T, doc string) map[string]interface{} {
	t.Helper()
	return toUnstructured(t, []byte(strings.TrimSpace(doc)))
}

const pipelineArgoCDPRReview = `
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: {name: app, namespace: default}
spec:
  git: {url: "https://github.com/example/gitops"}
  environments:
  - name: test
  - name: prod
    approval: pr-review
    update:
      strategy: argocd
      argocd: {application: app-prod}
`

func assertRejectedWith(t *testing.T, errs []string, want string) {
	t.Helper()
	require.Len(t, errs, 1, "%v", errs)
	assert.Contains(t, errs[0], want)
}

// TestCRDDropsRemovedFields: the fields v0.10.0 deprecated and v0.11.0
// removed are not in the schemas, so the API server refuses them with
// strict field validation and prunes them from stored objects.
// PolicyGate spec.when stays, with no default: the gate instances of a Graph
// built by an earlier release carry it, and kro compiles a Graph's templates
// against the schema (TestCRDKeepsGateWhen).
// Covers API-REMOVED-01.
func TestCRDDropsRemovedFields(t *testing.T) {
	crds := loadCRDs(t)
	pipeline := crds["Pipeline"].structural.Properties["spec"]
	env := pipeline.Properties["environments"].Items
	for _, c := range []struct {
		where  string
		props  map[string]structuralschema.Structural
		fields []string
	}{
		{"Pipeline spec", pipeline.Properties, []string{"policyGates"}},
		{"Pipeline spec.git", pipeline.Properties["git"].Properties, []string{"provider"}},
		{"Pipeline environments[]", env.Properties, []string{"shard", "steps", "promotionTemplate", "regions"}},
		{"Pipeline environments[].health", env.Properties["health"].Properties, []string{"cluster"}},
		{"PolicyGate spec", crds["PolicyGate"].structural.Properties["spec"].Properties, []string{"selector"}},
		{"NotificationHook spec.webhook", crds["NotificationHook"].structural.Properties["spec"].Properties["webhook"].Properties,
			[]string{"authorizationHeader"}},
		{"PromotionStep spec", crds["PromotionStep"].structural.Properties["spec"].Properties, []string{"region"}},
	} {
		for _, f := range c.fields {
			_, ok := c.props[f]
			assert.False(t, ok, "%s still has %s", c.where, f)
		}
	}
}

// TestCRDRejectsArgoCDWithPRReview: update.strategy argocd patches the
// Application directly, so approval: pr-review is refused at apply time with
// the message the argocd-set-image step uses (#1281).
func TestCRDRejectsArgoCDWithPRReview(t *testing.T) {
	crds := loadCRDs(t)
	p := crFromYAML(t, pipelineArgoCDPRReview)
	assertRejectedWith(t, celAdmission(t, crds, p, nil, false),
		"update.strategy argocd patches the Application directly and cannot honour approval: pr-review")

	ok := []struct {
		name string
		path []string
		val  interface{}
	}{
		{"argocd with auto", []string{"spec", "environments", "approval"}, "auto"},
		{"kustomize with pr-review", []string{"spec", "environments", "update", "strategy"}, "kustomize"},
		{"helm with pr-review", []string{"spec", "environments", "update", "strategy"}, "helm"},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			obj := crFromYAML(t, pipelineArgoCDPRReview)
			envs := obj["spec"].(map[string]interface{})["environments"].([]interface{})
			prod := envs[1].(map[string]interface{})
			setPath(prod, c.path[2:], c.val)
			assert.Empty(t, celAdmission(t, crds, obj, nil, false))
		})
	}
}

const pipelineArgoCDFleet = `
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: {name: app, namespace: default}
spec:
  git: {url: "https://github.com/example/gitops"}
  environments:
  - name: test
  - name: prod
    update:
      strategy: argocd
      argocd: {application: app-prod}
    fleet:
      targets: [{name: eu}, {name: us}]
`

// TestCRDRejectsArgoCDFleet: a fleet environment with update.strategy argocd
// is refused at apply time: every target would patch the fleet's one
// Application (#1847). The same fleet with a git-based strategy, and argocd
// on an environment that is not a fleet, are admitted. Covers
// API-ARGOCD-FLEET-01.
func TestCRDRejectsArgoCDFleet(t *testing.T) {
	crds := loadCRDs(t)
	assertRejectedWith(t, celAdmission(t, crds, crFromYAML(t, pipelineArgoCDFleet), nil, false),
		"update.strategy argocd cannot promote a fleet")

	for _, c := range []struct {
		name string
		edit func(prod map[string]interface{})
	}{
		{"fleet with kustomize", func(prod map[string]interface{}) {
			prod["update"] = map[string]interface{}{"strategy": "kustomize"}
		}},
		{"fleet with the default strategy", func(prod map[string]interface{}) { delete(prod, "update") }},
		{"argocd without a fleet", func(prod map[string]interface{}) { delete(prod, "fleet") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			obj := crFromYAML(t, pipelineArgoCDFleet)
			c.edit(obj["spec"].(map[string]interface{})["environments"].([]interface{})[1].(map[string]interface{}))
			assert.Empty(t, celAdmission(t, crds, obj, nil, false))
		})
	}
}

// TestCRDNewRulesRatchet: a Pipeline or PolicyGate stored before the new rules
// can still take edits that leave the rejected value alone (kardinal pause,
// kardinal override, status writes), because each rule sits on the smallest
// node that holds the value and ratcheting spares an unchanged node. An edit
// to that node must fix it.
func TestCRDNewRulesRatchet(t *testing.T) {
	crds := loadCRDs(t)

	tests := []struct {
		name   string
		stored string
		edit   func(obj map[string]interface{})
		want   string // "" = accepted with ratcheting
	}{
		{
			name:   "argocd + pr-review: pause",
			stored: pipelineArgoCDPRReview,
			edit:   func(o map[string]interface{}) { setPath(o, []string{"spec", "paused"}, true) },
		},
		{
			name:   "argocd + pr-review: edit another environment",
			stored: pipelineArgoCDPRReview,
			edit: func(o map[string]interface{}) {
				envs := o["spec"].(map[string]interface{})["environments"].([]interface{})
				envs[0].(map[string]interface{})["approval"] = "auto"
			},
		},
		{
			name:   "argocd + pr-review: edit the offending environment",
			stored: pipelineArgoCDPRReview,
			edit: func(o map[string]interface{}) {
				envs := o["spec"].(map[string]interface{})["environments"].([]interface{})
				envs[1].(map[string]interface{})["onHealthFailure"] = "rollback"
			},
			want: "cannot honour approval: pr-review",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := crFromYAML(t, tt.stored)
			updated := crFromYAML(t, tt.stored)
			tt.edit(updated)
			got := celAdmission(t, crds, updated, stored, true)
			if tt.want == "" {
				assert.Empty(t, got, "ratcheting must spare an unchanged node")
			} else {
				assertRejectedWith(t, got, tt.want)
			}
			assert.NotEmpty(t, celAdmission(t, crds, updated, stored, false),
				"without ratcheting the stored value fails every update")
		})
	}
}

// TestCRDAuditEventDropsBundleImage: bundleImage was never written (#1269);
// the schema no longer has it, so the API server prunes it. spec.actor came
// back in v0.12 as the structured actor (docs/actors.md): the never-written
// string form is refused.
func TestCRDAuditEventDropsBundleImage(t *testing.T) {
	crds := loadCRDs(t)
	ev := crFromYAML(t, `
apiVersion: kardinal.io/v1alpha1
kind: AuditEvent
metadata: {name: e, namespace: default}
spec:
  timestamp: "2026-09-30T00:00:00Z"
  bundleName: b
  pipelineName: p
  environment: prod
  action: PromotionStarted
  outcome: Success
  bundleImage: ghcr.io/example/app:1.0
`)
	assert.ElementsMatch(t, []string{"unknown field spec.bundleImage"}, validateCR(t, crds, ev))
	ev["spec"].(map[string]interface{})["actor"] = map[string]interface{}{"kind": "human", "principal": "alice"}
	assert.Empty(t, validateCR(t, crds, ev))
	ev["spec"].(map[string]interface{})["actor"] = "alice"
	assert.Contains(t, validateCR(t, crds, ev), `spec.actor in body must be of type object: "string"`)
}

// TestCRDKeepsGateWhen: PolicyGate spec.when stays in the schema, without a
// default, so a Graph built by v0.10.0 or earlier (its gate instances carry
// when: post-deploy) still compiles in kro after the upgrade.
func TestCRDKeepsGateWhen(t *testing.T) {
	when, ok := loadCRDs(t)["PolicyGate"].structural.Properties["spec"].Properties["when"]
	require.True(t, ok, "spec.when stays in the schema")
	assert.Nil(t, when.Default.Object, "spec.when has no default")
}
