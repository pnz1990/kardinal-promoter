// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

// Admission tests for fields that nothing reads (#1269, #1276) and for the
// argocd + pr-review combination the argocd strategy refuses (#1281).
//
// celAdmission runs the API server's own CEL validator
// (k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel), not the cel-go
// approximation in crd_schema_test.go, so it also covers CRD validation
// ratcheting: with ratcheting (the CRDValidationRatcheting gate, beta and on by
// default since Kubernetes 1.30, GA in 1.33) a rule error is dropped when the
// schema node that carries the rule is unchanged from the stored object.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

const pipelineWithPolicyGates = `
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: {name: app, namespace: default}
spec:
  git: {url: "https://github.com/example/gitops"}
  environments:
  - name: test
  - name: prod
  policyGates:
  - name: org-freeze
`

const policyGateWithSelector = `
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata: {name: freeze, namespace: platform-policies}
spec:
  expression: "!schedule.isWeekend"
  selector:
    matchLabels: {tier: prod}
`

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

// TestCRDRejectsUnreadFields: Pipeline.spec.policyGates and
// PolicyGate.spec.selector gate nothing, so the CRD refuses them on create
// (#1269).
func TestCRDRejectsUnreadFields(t *testing.T) {
	crds := loadCRDs(t)

	p := crFromYAML(t, pipelineWithPolicyGates)
	assertRejectedWith(t, celAdmission(t, crds, p, nil, false), "spec.policyGates is not implemented")
	setPath(p, []string{"spec", "policyGates"}, []interface{}{})
	assert.Empty(t, celAdmission(t, crds, p, nil, false), "an empty policyGates list gates nothing and is accepted")
	delete(p["spec"].(map[string]interface{}), "policyGates")
	assert.Empty(t, celAdmission(t, crds, p, nil, false))

	g := crFromYAML(t, policyGateWithSelector)
	assertRejectedWith(t, celAdmission(t, crds, g, nil, false), "spec.selector is not implemented; use the kardinal.io/applies-to label")
	setPath(g, []string{"spec", "selector"}, map[string]interface{}{})
	assertRejectedWith(t, celAdmission(t, crds, g, nil, false), "spec.selector is not implemented")
	delete(g["spec"].(map[string]interface{}), "selector")
	assert.Empty(t, celAdmission(t, crds, g, nil, false))
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

// TestCRDNewRulesRatchet: a Pipeline or PolicyGate stored before the new rules
// can still take edits that leave the rejected value alone (kardinal pause,
// kardinal override, status writes), because each rule sits on the smallest
// node that holds the value and ratcheting spares an unchanged node. An edit
// to that node must fix it. Without ratcheting (Kubernetes before 1.30, or the
// gate turned off) every update of such an object fails; the PR's upgrade note
// says to drop the field first.
func TestCRDNewRulesRatchet(t *testing.T) {
	crds := loadCRDs(t)

	tests := []struct {
		name   string
		stored string
		edit   func(obj map[string]interface{})
		want   string // "" = accepted with ratcheting
	}{
		{
			name:   "pipeline with policyGates: pause",
			stored: pipelineWithPolicyGates,
			edit:   func(o map[string]interface{}) { setPath(o, []string{"spec", "paused"}, true) },
		},
		{
			name:   "pipeline with policyGates: status write",
			stored: pipelineWithPolicyGates,
			edit:   func(o map[string]interface{}) { setPath(o, []string{"status", "phase"}, "Ready") },
		},
		{
			name:   "pipeline with policyGates: edit policyGates",
			stored: pipelineWithPolicyGates,
			edit: func(o map[string]interface{}) {
				setPath(o, []string{"spec", "policyGates"}, []interface{}{map[string]interface{}{"name": "other"}})
			},
			want: "spec.policyGates is not implemented",
		},
		{
			name:   "policygate with selector: edit expression",
			stored: policyGateWithSelector,
			edit:   func(o map[string]interface{}) { setPath(o, []string{"spec", "expression"}, "true") },
		},
		{
			name:   "policygate with selector: status write",
			stored: policyGateWithSelector,
			edit:   func(o map[string]interface{}) { setPath(o, []string{"status", "ready"}, true) },
		},
		{
			name:   "policygate with selector: edit selector",
			stored: policyGateWithSelector,
			edit: func(o map[string]interface{}) {
				setPath(o, []string{"spec", "selector", "matchLabels"}, map[string]interface{}{"tier": "staging"})
			},
			want: "spec.selector is not implemented",
		},
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

// TestCRDGitProviderHasNoDefault: spec.git.provider is ignored (#1276), so the
// CRD no longer stores a default for it, and it stays optional.
func TestCRDGitProviderHasNoDefault(t *testing.T) {
	git := loadCRDs(t)["Pipeline"].structural.Properties["spec"].Properties["git"]
	provider, ok := git.Properties["provider"]
	require.True(t, ok, "provider stays in the schema so stored Pipelines keep validating")
	assert.Nil(t, provider.Default.Object, "provider must not be defaulted")
	assert.Contains(t, provider.Description, "Deprecated: ignored; the controller's --scm-provider flag selects the provider.")
	assert.NotContains(t, git.ValueValidation.Required, "provider")
}

// TestCRDAuditEventDropsActorAndBundleImage: actor and bundleImage were never
// written (#1269); the schema no longer has them, so the API server prunes
// them.
func TestCRDAuditEventDropsActorAndBundleImage(t *testing.T) {
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
  actor: alice
  bundleImage: ghcr.io/example/app:1.0
`)
	errs := validateCR(t, crds, ev)
	assert.ElementsMatch(t, []string{"unknown field spec.actor", "unknown field spec.bundleImage"}, errs)
}
