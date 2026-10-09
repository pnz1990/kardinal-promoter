// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipelineWithPR is a Pipeline whose prod environment configures its PR.
const pipelineWithPR = `
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: {name: app, namespace: default}
spec:
  git: {url: "https://github.com/example/gitops"}
  environments:
  - name: test
  - name: prod
    approval: pr-review
    pr:
      titleTemplate: "deploy {{ .Bundle.Version }} to {{ .Environment }}"
      labels: ["env/{{ .Environment }}"]
      reviewers: [alice]
      teamReviewers: [platform]
      assignees: ["{{ .Bundle.Author }}"]
      merge: {auto: true, method: squash, commitMessageTemplate: "{{ .PR.Title }}"}
`

// TestCRDPipelinePRConfig checks the admission rules of environments[].pr
// (#1453): pr needs approval pr-review, merge.method and
// commitMessageTemplate need merge.auto, and the schema takes only the
// three merge methods.
//
// Covers SCM-PRCTL-CRD-01.
func TestCRDPipelinePRConfig(t *testing.T) {
	crds := loadCRDs(t)
	prod := func(obj map[string]interface{}) map[string]interface{} {
		return obj["spec"].(map[string]interface{})["environments"].([]interface{})[1].(map[string]interface{})
	}
	tests := []struct {
		name string
		edit func(env map[string]interface{})
		want string // "" = accepted
	}{
		{name: "full config", edit: func(map[string]interface{}) {}},
		{name: "auto approval", edit: func(env map[string]interface{}) { env["approval"] = "auto" },
			want: "environments[].pr configures the promotion pull request and needs approval: pr-review"},
		{name: "no approval", edit: func(env map[string]interface{}) { delete(env, "approval") },
			want: "needs approval: pr-review"},
		{name: "method without auto", edit: func(env map[string]interface{}) {
			env["pr"].(map[string]interface{})["merge"] = map[string]interface{}{"method": "rebase"}
		}, want: "set pr.merge.auto: true"},
		{name: "message without auto", edit: func(env map[string]interface{}) {
			env["pr"].(map[string]interface{})["merge"] = map[string]interface{}{"auto": false, "commitMessageTemplate": "x"}
		}, want: "set pr.merge.auto: true"},
		{name: "allowImmediate without auto", edit: func(env map[string]interface{}) {
			env["pr"].(map[string]interface{})["merge"] = map[string]interface{}{"allowImmediate": true}
		}, want: "set pr.merge.auto: true"},
		{name: "allowImmediate with auto", edit: func(env map[string]interface{}) {
			env["pr"].(map[string]interface{})["merge"] = map[string]interface{}{"auto": true, "allowImmediate": true}
		}},
		{name: "auto alone", edit: func(env map[string]interface{}) {
			env["pr"].(map[string]interface{})["merge"] = map[string]interface{}{"auto": true}
		}},
		{name: "no merge", edit: func(env map[string]interface{}) { delete(env["pr"].(map[string]interface{}), "merge") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := crFromYAML(t, pipelineWithPR)
			tt.edit(prod(obj))
			got := celAdmission(t, crds, obj, nil, false)
			if tt.want == "" {
				assert.Empty(t, got)
				assert.Empty(t, validateCR(t, crds, obj))
				return
			}
			assertRejectedWith(t, got, tt.want)
		})
	}

	t.Run("merge method enum", func(t *testing.T) {
		obj := crFromYAML(t, pipelineWithPR)
		prod(obj)["pr"].(map[string]interface{})["merge"].(map[string]interface{})["method"] = "fast-forward"
		errs := validateCR(t, crds, obj)
		require.NotEmpty(t, errs)
		assert.Contains(t, strings.Join(errs, "\n"), "pr.merge.method in body should be one of [merge squash rebase]")
	})
	t.Run("list bound", func(t *testing.T) {
		obj := crFromYAML(t, pipelineWithPR)
		many := make([]interface{}, 21)
		for i := range many {
			many[i] = "r"
		}
		prod(obj)["pr"].(map[string]interface{})["reviewers"] = many
		errs := validateCR(t, crds, obj)
		require.NotEmpty(t, errs)
		assert.Contains(t, strings.Join(errs, "\n"), "reviewers")
	})
}
