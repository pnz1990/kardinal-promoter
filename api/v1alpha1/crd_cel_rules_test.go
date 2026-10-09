// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// crdSchema returns the openAPIV3Schema node at the given property path of the
// first version of a generated CRD in config/crd/bases. A path element "[]"
// descends into array items.
func crdSchema(t *testing.T, file string, path ...string) map[string]interface{} {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "config", "crd", "bases", file))
	require.NoError(t, err)
	var crd map[string]interface{}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	node := crd["spec"].(map[string]interface{})["versions"].([]interface{})[0].(map[string]interface{})
	node = node["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})
	for _, p := range path {
		if p == "[]" {
			node = node["items"].(map[string]interface{})
			continue
		}
		node = node["properties"].(map[string]interface{})[p].(map[string]interface{})
	}
	return node
}

// failingRules evaluates the x-kubernetes-validations rules of a schema node
// against self (a JSON-shaped value) and returns the messages of the rules that
// reject it. It uses cel-go directly with self as dyn, which is sufficient for
// the has()/comparison rules checked here.
func failingRules(t *testing.T, node map[string]interface{}, self map[string]interface{}) []string {
	t.Helper()
	rules, _ := node["x-kubernetes-validations"].([]interface{})
	require.NotEmpty(t, rules, "schema node has no x-kubernetes-validations")
	env, err := cel.NewEnv(cel.Variable("self", cel.DynType))
	require.NoError(t, err)
	var failed []string
	for _, r := range rules {
		rule := r.(map[string]interface{})
		if strings.Contains(rule["rule"].(string), "oldSelf") {
			continue // a transition rule: only checked on update, see TestBundleCRDRejectedIsOneWay
		}
		ast, iss := env.Compile(rule["rule"].(string))
		require.NoError(t, iss.Err(), "rule %q", rule["rule"])
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(map[string]interface{}{"self": self})
		require.NoError(t, err, "rule %q", rule["rule"])
		if out.Value() != true {
			failed = append(failed, rule["message"].(string))
		}
	}
	return failed
}

// TestChangeWindowCRDRules verifies the ChangeWindow admission rules: a blackout
// needs start and end, a recurring window needs a schedule, and allowedHours must
// be HH:MM-HH:MM (C04-gates-03).
func TestChangeWindowCRDRules(t *testing.T) {
	spec := crdSchema(t, "kardinal.io_changewindows.yaml", "spec")
	tests := []struct {
		name string
		self map[string]interface{}
		want []string
	}{
		{"blackout ok", map[string]interface{}{"type": "blackout", "start": "2026-12-20T00:00:00Z", "end": "2027-01-02T00:00:00Z"}, nil},
		{"blackout without end", map[string]interface{}{"type": "blackout", "start": "2026-12-20T00:00:00Z"},
			[]string{"a blackout ChangeWindow requires start and end"}},
		{"recurring ok", map[string]interface{}{"type": "recurring", "schedule": map[string]interface{}{"allowedHours": "09:00-17:00"}}, nil},
		{"recurring without schedule", map[string]interface{}{"type": "recurring"},
			[]string{"a recurring ChangeWindow requires schedule"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, failingRules(t, spec, tt.self))
		})
	}

	hours := crdSchema(t, "kardinal.io_changewindows.yaml", "spec", "schedule", "allowedHours")
	re := regexp.MustCompile(hours["pattern"].(string))
	for _, ok := range []string{"09:00-17:00", "22:00-02:00", "00:00-24:00"} {
		assert.True(t, re.MatchString(ok), ok)
	}
	for _, bad := range []string{"9:00-17:00", "09:00-24:30", "24:00-09:00", "09:00", "9am-5pm"} {
		assert.False(t, re.MatchString(bad), bad)
	}
}

// TestPipelineCRDRejectsAutoRollback verifies that environments[].autoRollback,
// which has no implementation, is rejected loudly instead of silently ignored
// (C04-gates-07, C08-api-config-14).
func TestPipelineCRDRejectsAutoRollback(t *testing.T) {
	env := crdSchema(t, "kardinal.io_pipelines.yaml", "spec", "environments", "[]")
	assert.Empty(t, failingRules(t, env, map[string]interface{}{"name": "prod", "onHealthFailure": "rollback"}))
	failed := failingRules(t, env, map[string]interface{}{
		"name": "prod", "autoRollback": map[string]interface{}{"failureThreshold": 3},
	})
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0], "environments[].autoRollback is not implemented")
}

// TestPipelineCRDRejectsStepsAndPromotionTemplate verifies that the API server
// rejects the deprecated environments[].steps and promotionTemplate fields
// (#1282): kardinal has no custom step engine, and the PromotionTemplate CRD
// was removed. An empty steps list is accepted, as Build accepts it, so a
// stored Pipeline that never set the fields keeps updating.
func TestPipelineCRDRejectsStepsAndPromotionTemplate(t *testing.T) {
	env := crdSchema(t, "kardinal.io_pipelines.yaml", "spec", "environments", "[]")
	tests := []struct {
		name string
		self map[string]interface{}
		want string
	}{
		{name: "neither field", self: map[string]interface{}{"name": "prod", "approval": "pr-review"}},
		{name: "empty steps", self: map[string]interface{}{"name": "prod", "steps": []interface{}{}}},
		{name: "steps", self: map[string]interface{}{
			"name": "prod", "steps": []interface{}{map[string]interface{}{"uses": "git-clone"}},
		}, want: "environments[].steps is not supported"},
		{name: "promotionTemplate", self: map[string]interface{}{
			"name": "prod", "promotionTemplate": map[string]interface{}{"name": "standard"},
		}, want: "environments[].promotionTemplate is not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failed := failingRules(t, env, tt.self)
			if tt.want == "" {
				assert.Empty(t, failed)
				return
			}
			require.Len(t, failed, 1)
			assert.Contains(t, failed[0], tt.want)
			assert.Contains(t, failed[0], "docs/pipeline-reference.md#promotion-steps")
		})
	}
}

// TestPromotionTemplateAndStepInputsRemoved verifies that the PromotionTemplate
// CRD and PromotionStep spec.inputs are gone (#1282): no CRD file in config or
// the chart, no kind in the scheme, and no inputs property in the PromotionStep
// schema, so the API server prunes the field.
func TestPromotionTemplateAndStepInputsRemoved(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	for _, dir := range []string{"config/crd/bases", "chart/kardinal-promoter/crds"} {
		_, err := os.Stat(filepath.Join(root, dir, "kardinal.io_promotiontemplates.yaml"))
		assert.True(t, os.IsNotExist(err), "%s still has the PromotionTemplate CRD", dir)
	}

	scheme := k8sruntime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	assert.True(t, scheme.Recognizes(v1alpha1.GroupVersion.WithKind("PromotionStep")))
	assert.False(t, scheme.Recognizes(v1alpha1.GroupVersion.WithKind("PromotionTemplate")))

	spec := crdSchema(t, "kardinal.io_promotionsteps.yaml", "spec")
	assert.NotContains(t, spec["properties"], "inputs")
}

// TestNotificationHookCRDRules verifies the NotificationHook admission rules:
// a webhook needs a url or a secretRef, authorizationHeader and secretRef
// are exclusive, and spec.template comes with format: template only.
func TestNotificationHookCRDRules(t *testing.T) {
	webhook := crdSchema(t, "kardinal.io_notificationhooks.yaml", "spec", "webhook")
	ref := map[string]interface{}{"name": "creds"}
	whTests := []struct {
		name string
		self map[string]interface{}
		want []string
	}{
		{"url", map[string]interface{}{"url": "https://x"}, nil},
		{"secretRef", map[string]interface{}{"secretRef": ref}, nil},
		{"url and secretRef", map[string]interface{}{"url": "https://x", "secretRef": ref}, nil},
		{"plaintext header", map[string]interface{}{"url": "https://x", "authorizationHeader": "Bearer t"}, nil},
		{"nothing", map[string]interface{}{}, []string{"webhook: set url, or secretRef with a url key"}},
		{"empty url", map[string]interface{}{"url": ""}, []string{"webhook: set url, or secretRef with a url key"}},
		{"header and secretRef", map[string]interface{}{"secretRef": ref, "authorizationHeader": "Bearer t"},
			[]string{"webhook: authorizationHeader and secretRef are mutually exclusive; move the header into the Secret's authorization key"}},
	}
	for _, tt := range whTests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, failingRules(t, webhook, tt.self))
		})
	}

	spec := crdSchema(t, "kardinal.io_notificationhooks.yaml", "spec")
	tmpl := map[string]interface{}{"body": "{{ .Event }}"}
	const msg = "template is required with format: template and not allowed with any other format"
	specTests := []struct {
		name string
		self map[string]interface{}
		want []string
	}{
		{"default format", map[string]interface{}{}, nil},
		{"slack", map[string]interface{}{"format": "slack"}, nil},
		{"template with body", map[string]interface{}{"format": "template", "template": tmpl}, nil},
		{"template without body", map[string]interface{}{"format": "template"}, []string{msg}},
		{"body without format", map[string]interface{}{"template": tmpl}, []string{msg}},
		{"body with teams", map[string]interface{}{"format": "teams", "template": tmpl}, []string{msg}},
	}
	for _, tt := range specTests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, failingRules(t, spec, tt.self))
		})
	}

	ct := crdSchema(t, "kardinal.io_notificationhooks.yaml", "spec", "template", "contentType")
	re := regexp.MustCompile(ct["pattern"].(string))
	for _, ok := range []string{"application/json", "text/plain; charset=utf-8", "application/vnd.api+json"} {
		assert.True(t, re.MatchString(ok), ok)
	}
	for _, bad := range []string{"json", "text/plain\r\nX-Evil: 1", "/json", ""} {
		assert.False(t, re.MatchString(bad), "%q", bad)
	}
}

// TestBundleCRDRejectsConfigRefOnImage: the API server refuses an image
// Bundle with a configRef, which it would deploy without (#1353). The
// Bundle API and kardinal create bundle refuse it first with the same advice.
func TestBundleCRDRejectsConfigRefOnImage(t *testing.T) {
	spec := crdSchema(t, "kardinal.io_bundles.yaml", "spec")
	const msg = "spec.configRef is used only by config and mixed Bundles: an image Bundle deploys only its images; " +
		"set type config or mixed, or remove configRef"
	ref := map[string]interface{}{"commitSHA": "abc"}
	img := []interface{}{map[string]interface{}{"repository": "r/app", "tag": "1"}}
	tests := []struct {
		name string
		self map[string]interface{}
		want []string
	}{
		{"image with configRef", map[string]interface{}{"type": "image", "pipeline": "p", "images": img, "configRef": ref}, []string{msg}},
		{"image with an empty configRef", map[string]interface{}{"type": "image", "pipeline": "p", "images": img,
			"configRef": map[string]interface{}{}}, []string{msg}},
		{"image", map[string]interface{}{"type": "image", "pipeline": "p", "images": img}, nil},
		{"config", map[string]interface{}{"type": "config", "pipeline": "p", "configRef": ref}, nil},
		{"mixed", map[string]interface{}{"type": "mixed", "pipeline": "p", "images": img, "configRef": ref}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, failingRules(t, spec, tc.self))
		})
	}
}

// TestBundleCRDRejectedIsOneWay verifies the Bundle spec transition rules for
// spec.rejected (#1451): it can be set once, and never changed or removed.
func TestBundleCRDRejectedIsOneWay(t *testing.T) {
	spec := crdSchema(t, "kardinal.io_bundles.yaml", "spec")
	var rules []interface{}
	for _, r := range spec["x-kubernetes-validations"].([]interface{}) {
		if strings.Contains(r.(map[string]interface{})["rule"].(string), "oldSelf") {
			rules = append(rules, r)
		}
	}
	require.Len(t, rules, 2)
	env, err := cel.NewEnv(cel.Variable("self", cel.DynType), cel.Variable("oldSelf", cel.DynType))
	require.NoError(t, err)
	failing := func(old, cur map[string]interface{}) []string {
		var failed []string
		for _, r := range rules {
			rule := r.(map[string]interface{})
			ast, iss := env.Compile(rule["rule"].(string))
			require.NoError(t, iss.Err(), "rule %q", rule["rule"])
			prg, err := env.Program(ast)
			require.NoError(t, err)
			out, _, err := prg.Eval(map[string]interface{}{"self": cur, "oldSelf": old})
			require.NoError(t, err, "rule %q", rule["rule"])
			if out.Value() != true {
				failed = append(failed, rule["message"].(string))
			}
		}
		return failed
	}
	rej := map[string]interface{}{"by": "alice", "reason": "bad"}
	other := map[string]interface{}{"by": "bob", "reason": "bad"}
	base := func(r map[string]interface{}) map[string]interface{} {
		m := map[string]interface{}{"type": "image", "pipeline": "app"}
		if r != nil {
			m["rejected"] = r
		}
		return m
	}
	tests := []struct {
		name     string
		old, cur map[string]interface{}
		want     []string
	}{
		{name: "set", old: base(nil), cur: base(rej)},
		{name: "unchanged", old: base(rej), cur: base(rej)},
		{name: "no rejection", old: base(nil), cur: base(nil)},
		{name: "removed", old: base(rej), cur: base(nil),
			want: []string{"spec.rejected cannot be removed: a rejected Bundle stays rejected"}},
		{name: "changed", old: base(rej), cur: base(other), want: []string{"spec.rejected is immutable once set"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, failing(tc.old, tc.cur))
		})
	}
	assert.Contains(t, crdSchema(t, "kardinal.io_bundles.yaml", "status", "phase")["enum"], "Rejected")
}
