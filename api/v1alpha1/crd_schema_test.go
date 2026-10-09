// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

// Offline CRD admission checks. validateCR does what the API server does to a
// custom resource on create, using the generated CRDs in config/crd/bases:
// prune unknown fields, validate types/enums/patterns/lengths (kube-openapi),
// check list-map keys for duplicates, and evaluate x-kubernetes-validations
// rules with cel-go. A pruned field is reported as an error so that tests catch
// fields the API server would silently drop.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"sigs.k8s.io/yaml"
)

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

type loadedCRD struct {
	crd        apiextensionsv1.CustomResourceDefinition
	structural *structuralschema.Structural
}

// loadCRDs returns the generated CRDs keyed by kind.
func loadCRDs(t *testing.T) map[string]loadedCRD {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRootDir(t), "config", "crd", "bases", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	out := map[string]loadedCRD{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var crd apiextensionsv1.CustomResourceDefinition
		require.NoError(t, yaml.Unmarshal(raw, &crd), f)
		require.Len(t, crd.Spec.Versions, 1, "%s: validateCR assumes one version", f)
		var internal apiextensionsinternal.JSONSchemaProps
		require.NoError(t, apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
			crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil))
		s, err := structuralschema.NewStructural(&internal)
		require.NoError(t, err, f)
		out[crd.Spec.Names.Kind] = loadedCRD{crd: crd, structural: s}
	}
	return out
}

// toUnstructured decodes YAML the way the API server decodes JSON: whole
// numbers become int64, not float64.
func toUnstructured(t *testing.T, doc []byte) map[string]interface{} {
	t.Helper()
	j, err := yaml.YAMLToJSON(doc)
	require.NoError(t, err)
	var obj map[string]interface{}
	require.NoError(t, utiljson.Unmarshal(j, &obj))
	return obj
}

// validateCR returns every reason the API server would reject obj (or drop
// part of it).
func validateCR(t *testing.T, crds map[string]loadedCRD, obj map[string]interface{}) []string {
	t.Helper()
	kind, _ := obj["kind"].(string)
	c, ok := crds[kind]
	require.True(t, ok, "no CRD for kind %q", kind)
	s := c.structural

	var errs []string
	for _, p := range pruning.PruneWithOptions(obj, s, true, structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true}) {
		errs = append(errs, "unknown field "+p)
	}
	res := validate.NewSchemaValidator(s.ToKubeOpenAPI(), nil, "", strfmt.Default).Validate(obj)
	for _, e := range res.Errors {
		errs = append(errs, e.Error())
	}
	for _, e := range listtype.ValidateListSetsAndMaps(field.NewPath(""), s, obj) {
		errs = append(errs, e.Error())
	}
	errs = append(errs, celRuleErrors(t, s, obj, "")...)
	return errs
}

// celRuleErrors evaluates every x-kubernetes-validations rule in s against the
// matching node of obj. It covers what our rules use (self, has, in); the API
// server's environment adds more library functions and a cost check.
// Transition rules (those using oldSelf) are skipped, as on create.
func celRuleErrors(t *testing.T, s *structuralschema.Structural, obj interface{}, path string) []string {
	t.Helper()
	if s == nil || obj == nil {
		return nil
	}
	var errs []string
	for _, r := range s.XValidations {
		if strings.Contains(r.Rule, "oldSelf") {
			continue
		}
		env, err := cel.NewEnv(cel.Variable("self", cel.DynType))
		require.NoError(t, err)
		ast, iss := env.Compile(r.Rule)
		require.NoError(t, iss.Err(), "rule at %q does not compile: %s", path, r.Rule)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(map[string]interface{}{"self": obj})
		if err != nil || out.Value() != true {
			errs = append(errs, fmt.Sprintf("%s: %s", path, r.Message))
		}
	}
	switch v := obj.(type) {
	case map[string]interface{}:
		for k, val := range v {
			if p, ok := s.Properties[k]; ok {
				errs = append(errs, celRuleErrors(t, &p, val, path+"."+k)...)
			} else if s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil {
				errs = append(errs, celRuleErrors(t, s.AdditionalProperties.Structural, val, path+"."+k)...)
			}
		}
	case []interface{}:
		for i, it := range v {
			errs = append(errs, celRuleErrors(t, s.Items, it, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return errs
}

// yamlDocuments splits a multi-document YAML file.
func yamlDocuments(t *testing.T, file string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	var out [][]byte
	for _, d := range bytes.Split(raw, []byte("\n---")) {
		if len(bytes.TrimSpace(d)) > 0 {
			out = append(out, d)
		}
	}
	return out
}

// ── C08-api-config-22: samples pass the CRD schema ────────────────────────────

// TestCRDSchemaAcceptsSamples: every kardinal.io object in config/samples must
// be accepted by its CRD with nothing pruned.
func TestCRDSchemaAcceptsSamples(t *testing.T) {
	crds := loadCRDs(t)
	var files []string
	require.NoError(t, filepath.Walk(filepath.Join(repoRootDir(t), "config", "samples"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".yaml") {
			files = append(files, p)
		}
		return err
	}))
	require.NotEmpty(t, files)
	checked := 0
	for _, f := range files {
		for _, doc := range yamlDocuments(t, f) {
			obj := toUnstructured(t, doc)
			if obj == nil || !strings.HasPrefix(fmt.Sprint(obj["apiVersion"]), "kardinal.io/") {
				continue
			}
			checked++
			assert.Empty(t, validateCR(t, crds, obj), "%s: %s %v", filepath.Base(f), obj["kind"], obj["metadata"])
		}
	}
	assert.Greater(t, checked, 5)
}

// ── C08-api-config-33: environment names ──────────────────────────────────────

func pipelineWithEnvs(names ...string) map[string]interface{} {
	envs := make([]interface{}, 0, len(names))
	for _, n := range names {
		envs = append(envs, map[string]interface{}{"name": n})
	}
	return map[string]interface{}{
		"apiVersion": "kardinal.io/v1alpha1",
		"kind":       "Pipeline",
		"metadata":   map[string]interface{}{"name": "p", "namespace": "default"},
		"spec": map[string]interface{}{
			"git":          map[string]interface{}{"url": "https://github.com/example/gitops"},
			"environments": envs,
		},
	}
}

// TestCRDSchemaEnvironmentNames: an environment name becomes a kro node ID
// (camelCase), a Kubernetes name part and a Job namespace, so the CRD must
// reject names kro reserves, names that are not DNS labels, and duplicates.
func TestCRDSchemaEnvironmentNames(t *testing.T) {
	crds := loadCRDs(t)
	accepted := [][]string{
		{"test", "uat", "prod"},
		{"prod-eu", "prod-us"},
		{"x1", "stage-2"},
		{strings.Repeat("a", 63)},
	}
	for _, names := range accepted {
		assert.Empty(t, validateCR(t, crds, pipelineWithEnvs(names...)), "%v must be accepted", names)
	}
	rejected := [][]string{
		{"Prod"},
		{"prod_eu"},
		{"prod.eu"},
		{"-prod"},
		{"prod-"},
		{strings.Repeat("a", 64)},
		{"test", "test"},
		// kro reserved node IDs (compiler/validation.go) and kardinal's own "bundle" node.
		{"bundle"}, {"status"}, {"spec"}, {"metadata"}, {"kind"}, {"api-version"},
		{"graph"}, {"kro"}, {"self"}, {"each"}, {"item"}, {"items"}, {"object"},
		{"this"}, {"context"}, {"namespace"}, {"in"}, {"true"}, {"null"}, {"if"}, {"while"},
		{"time"},
	}
	for _, names := range rejected {
		assert.NotEmpty(t, validateCR(t, crds, pipelineWithEnvs(names...)), "%v must be rejected", names)
	}
}

// ── C08-api-config-30, -21: duration fields ───────────────────────────────────

func setPath(obj map[string]interface{}, path []string, value interface{}) {
	m := obj
	for i, p := range path {
		if i == len(path)-1 {
			m[p] = value
			return
		}
		if list, ok := m[p].([]interface{}); ok {
			m = list[0].(map[string]interface{})
			continue
		}
		next, ok := m[p].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[p] = next
		}
		m = next
	}
}

func baseObject(kind string) map[string]interface{} {
	meta := map[string]interface{}{"name": "x", "namespace": "default"}
	obj := map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": kind, "metadata": meta}
	switch kind {
	case "Pipeline":
		return pipelineWithEnvs("test")
	case "PolicyGate":
		obj["spec"] = map[string]interface{}{"expression": "true"}
	case "ScheduleClock":
		obj["spec"] = map[string]interface{}{}
	case "MetricCheck":
		obj["spec"] = map[string]interface{}{
			"provider": "prometheus", "prometheusURL": "http://prometheus:9090", "query": "up",
			"threshold": map[string]interface{}{"value": int64(1), "operator": "gte"},
		}
	case "HookRun":
		obj["spec"] = map[string]interface{}{
			"pipelineName": "p", "bundleName": "b", "environment": "prod", "hook": "migrate", "phase": "pre",
			"job": map[string]interface{}{"template": map[string]interface{}{}},
		}
	case "Subscription":
		obj["spec"] = map[string]interface{}{
			"type": "image", "pipeline": "p",
			"image": map[string]interface{}{"registry": "ghcr.io/example/app"},
			"git":   map[string]interface{}{"repoURL": "https://github.com/example/app"},
		}
	}
	return obj
}

// TestCRDSchemaDurationFields: every Go-duration string field rejects values
// time.ParseDuration rejects (the reconcilers silently fell back to a default)
// and accepts every value it accepts, including "1h30m" and "1.5h" (the old
// PolicyGate VAP regex rejected those).
func TestCRDSchemaDurationFields(t *testing.T) {
	crds := loadCRDs(t)
	fields := []struct {
		kind string
		path []string
	}{
		{"Pipeline", []string{"spec", "environments", "health", "timeout"}},
		{"Pipeline", []string{"spec", "environments", "waitForMergeTimeout"}},
		{"PolicyGate", []string{"spec", "recheckInterval"}},
		{"ScheduleClock", []string{"spec", "interval"}},
		{"MetricCheck", []string{"spec", "interval"}},
		{"Subscription", []string{"spec", "image", "interval"}},
		{"Subscription", []string{"spec", "git", "interval"}},
		{"HookRun", []string{"spec", "timeout"}},
	}
	good := []string{"", "0", "30s", "5m", "1h", "1h30m", "1.5h", "500ms", "2h45m30s", "10us", "10µs"}
	bad := []string{"15 minutes", "2 days", "5", "1d", "-5m", "5M", "1h 30m", "m"}
	for _, f := range fields {
		base := baseObject(f.kind)
		require.Empty(t, validateCR(t, crds, base), "%s base object must be valid", f.kind)
		for _, v := range good {
			obj := baseObject(f.kind)
			setPath(obj, f.path, v)
			assert.Empty(t, validateCR(t, crds, obj), "%s %s=%q must be accepted", f.kind, strings.Join(f.path, "."), v)
		}
		for _, v := range bad {
			obj := baseObject(f.kind)
			setPath(obj, f.path, v)
			assert.NotEmpty(t, validateCR(t, crds, obj), "%s %s=%q must be rejected", f.kind, strings.Join(f.path, "."), v)
		}
	}
}

// ── C08-api-config-04: the schema covers the removed VAP's valid rules ───────

// TestCRDSchemaCoversRemovedVAPRules: the chart's ValidatingAdmissionPolicies
// were removed (they denied every Pipeline). Each rule that was correct is
// enforced by the CRD schema; the wrong ones (per-environment gitRepo; images
// required on every image Bundle) are not.
func TestCRDSchemaCoversRemovedVAPRules(t *testing.T) {
	crds := loadCRDs(t)
	bundle := func(spec map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
			"metadata": map[string]interface{}{"name": "b", "namespace": "default"},
			"spec":     spec,
		}
	}
	gate := func(spec map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1", "kind": "PolicyGate",
			"metadata": map[string]interface{}{"name": "g", "namespace": "default"},
			"spec":     spec,
		}
	}
	env := func(e map[string]interface{}) map[string]interface{} {
		p := pipelineWithEnvs("test")
		p["spec"].(map[string]interface{})["environments"] = []interface{}{e}
		return p
	}
	rejected := map[string]map[string]interface{}{
		"policygate empty expression":    gate(map[string]interface{}{"expression": ""}),
		"policygate bad recheckInterval": gate(map[string]interface{}{"expression": "true", "recheckInterval": "5 minutes"}),
		"pipeline no environments":       pipelineWithEnvs(),
		"pipeline env empty name":        pipelineWithEnvs(""),
		"pipeline bad update strategy":   env(map[string]interface{}{"name": "test", "update": map[string]interface{}{"strategy": "custom"}}),
		"pipeline bad approval":          env(map[string]interface{}{"name": "test", "approval": "manual"}),
		"bundle empty pipeline":          bundle(map[string]interface{}{"type": "image", "pipeline": ""}),
		"bundle bad type":                bundle(map[string]interface{}{"type": "tarball", "pipeline": "p"}),
	}
	for name, obj := range rejected {
		assert.NotEmpty(t, validateCR(t, crds, obj), "%s must be rejected by the CRD schema", name)
	}
	accepted := map[string]map[string]interface{}{
		"quickstart-style pipeline": env(map[string]interface{}{
			"name": "test", "approval": "auto", "path": "environments/test",
			"update": map[string]interface{}{"strategy": "kustomize"},
		}),
		"rollback bundle without images": bundle(map[string]interface{}{"type": "image", "pipeline": "p"}),
		"gate with compound interval":    gate(map[string]interface{}{"expression": "true", "recheckInterval": "1h30m"}),
	}
	for name, obj := range accepted {
		assert.Empty(t, validateCR(t, crds, obj), "%s must be accepted", name)
	}
}

// ── C08-api-config-26: AuditEvent spec is immutable ──────────────────────────

// TestCRDAuditEventSpecImmutable: AuditEvents are an append-only audit trail,
// so an update that changes spec must be rejected (a status-free kind has no
// other field to update, and metadata edits stay allowed).
func TestCRDAuditEventSpecImmutable(t *testing.T) {
	spec := loadCRDs(t)["AuditEvent"].structural.Properties["spec"]
	var rule string
	for _, r := range spec.XValidations {
		if strings.Contains(r.Rule, "oldSelf") {
			rule = r.Rule
		}
	}
	require.NotEmpty(t, rule, "AuditEvent spec needs a transition rule")
	env, err := cel.NewEnv(cel.Variable("self", cel.DynType), cel.Variable("oldSelf", cel.DynType))
	require.NoError(t, err)
	ast, iss := env.Compile(rule)
	require.NoError(t, iss.Err())
	prg, err := env.Program(ast)
	require.NoError(t, err)
	old := map[string]interface{}{"bundleName": "b1", "pipelineName": "p", "action": "PromotionStarted", "outcome": "Success"}
	cases := []struct {
		name  string
		new   map[string]interface{}
		allow bool
	}{
		{"unchanged", map[string]interface{}{"bundleName": "b1", "pipelineName": "p", "action": "PromotionStarted", "outcome": "Success"}, true},
		{"outcome rewritten", map[string]interface{}{"bundleName": "b1", "pipelineName": "p", "action": "PromotionStarted", "outcome": "Failure"}, false},
		{"field removed", map[string]interface{}{"bundleName": "b1", "pipelineName": "p", "action": "PromotionStarted"}, false},
	}
	for _, c := range cases {
		out, _, err := prg.Eval(map[string]interface{}{"self": c.new, "oldSelf": old})
		require.NoError(t, err, c.name)
		assert.Equal(t, c.allow, out.Value(), c.name)
	}
}

// TestCRDHookRunPhaseLatched: the API server refuses to change a HookRun's
// phase once it is Succeeded, Failed or Skipped (regression, QA #1493: a
// reconcile from a stale copy overwrote Succeeded with Failed).
func TestCRDHookRunPhaseLatched(t *testing.T) {
	phase := loadCRDs(t)["HookRun"].structural.Properties["status"].Properties["phase"]
	var rule string
	for _, r := range phase.XValidations {
		if strings.Contains(r.Rule, "oldSelf") {
			rule = r.Rule
		}
	}
	require.NotEmpty(t, rule, "HookRun status.phase needs a transition rule")
	env, err := cel.NewEnv(cel.Variable("self", cel.StringType), cel.Variable("oldSelf", cel.StringType))
	require.NoError(t, err)
	ast, iss := env.Compile(rule)
	require.NoError(t, iss.Err())
	prg, err := env.Program(ast)
	require.NoError(t, err)
	cases := []struct {
		old, new string
		allow    bool
	}{
		{"Pending", "Running", true},
		{"Running", "Succeeded", true},
		{"Running", "Failed", true},
		{"Pending", "Skipped", true},
		{"Succeeded", "Succeeded", true},
		{"Succeeded", "Failed", false},
		{"Failed", "Succeeded", false},
		{"Skipped", "Running", false},
	}
	for _, c := range cases {
		out, _, err := prg.Eval(map[string]interface{}{"self": c.new, "oldSelf": c.old})
		require.NoError(t, err)
		assert.Equal(t, c.allow, out.Value(), "%s -> %s", c.old, c.new)
	}
}

// TestCRDImageVerificationPhaseLatched: the API server refuses to change a
// Verified or Failed ImageVerification's phase.
func TestCRDImageVerificationPhaseLatched(t *testing.T) {
	phase := loadCRDs(t)["ImageVerification"].structural.Properties["status"].Properties["phase"]
	var rule string
	for _, r := range phase.XValidations {
		if strings.Contains(r.Rule, "oldSelf") {
			rule = r.Rule
		}
	}
	require.NotEmpty(t, rule)
	env, err := cel.NewEnv(cel.Variable("self", cel.StringType), cel.Variable("oldSelf", cel.StringType))
	require.NoError(t, err)
	ast, iss := env.Compile(rule)
	require.NoError(t, iss.Err())
	prg, err := env.Program(ast)
	require.NoError(t, err)
	for _, c := range []struct {
		old, new string
		allow    bool
	}{{"Pending", "Verified", true}, {"Pending", "Failed", true}, {"Verified", "Failed", false}, {"Failed", "Verified", false}} {
		out, _, err := prg.Eval(map[string]interface{}{"self": c.new, "oldSelf": c.old})
		require.NoError(t, err)
		assert.Equal(t, c.allow, out.Value(), "%s -> %s", c.old, c.new)
	}
}

// TestCRDImageVerificationSpecImmutable: an ImageVerification's spec cannot
// change; a policy change gives a new one (QA #1521: an edited spec would
// keep the old verdict).
func TestCRDImageVerificationSpecImmutable(t *testing.T) {
	spec := loadCRDs(t)["ImageVerification"].structural.Properties["spec"]
	var rules []string
	for _, r := range spec.XValidations {
		rules = append(rules, r.Rule)
	}
	assert.Contains(t, rules, "self == oldSelf")
}

// ── C08-api-config-24, -28: printer columns, enums, short names ──────────────

// listFilter matches a JSONPath list filter such as [?(@.type=="Ready")],
// the usual form of a condition printer column.
var listFilter = regexp.MustCompile(`\[\?\(@\.([A-Za-z0-9_]+)=="[^"]*"\)\]`)

// TestCRDPrinterColumnsResolve: every printer column's JSONPath must name a
// field in the schema whose type matches the column type. A list filter
// ([?(@.type=="Valid")]) resolves to the list's items, and its key must be an
// item field.
func TestCRDPrinterColumnsResolve(t *testing.T) {
	for kind, c := range loadCRDs(t) {
		for _, col := range c.crd.Spec.Versions[0].AdditionalPrinterColumns {
			if strings.HasPrefix(col.JSONPath, ".metadata.") {
				continue
			}
			// Rewrite each filter to "[key]" so that the path splits on ".".
			path := listFilter.ReplaceAllString(strings.TrimPrefix(col.JSONPath, "."), "[$1]")
			s := c.structural
			for _, part := range strings.Split(path, ".") {
				name, filterKey, filtered := strings.Cut(strings.TrimSuffix(part, "]"), "[")
				next, ok := s.Properties[name]
				if !assert.True(t, ok, "%s column %q: %s does not resolve at %q", kind, col.Name, col.JSONPath, name) {
					s = nil
					break
				}
				s = &next
				if !filtered {
					continue
				}
				if !assert.True(t, s.Type == "array" && s.Items != nil,
					"%s column %q: %s filters %q, which is not a list", kind, col.Name, col.JSONPath, name) {
					s = nil
					break
				}
				s = s.Items
				_, ok = s.Properties[filterKey]
				if !assert.True(t, ok, "%s column %q: %s filters on %q, which %q items do not have",
					kind, col.Name, col.JSONPath, filterKey, name) {
					s = nil
					break
				}
			}
			if s == nil {
				continue
			}
			want := map[string][]string{
				"string": {"string"}, "date": {"string"}, "integer": {"integer"},
				"number": {"number", "integer"}, "boolean": {"boolean"},
			}[col.Type]
			assert.Contains(t, want, s.Type, "%s column %q is type %s over a %s field", kind, col.Name, col.Type, s.Type)
		}
	}
}

// TestPromotionStepStateEnum: the documented PromotionStep states must equal
// the CRD enum (a state the reconciler writes that the enum lacks is rejected
// by the API server on the status update).
func TestPromotionStepStateEnum(t *testing.T) {
	s := loadCRDs(t)["PromotionStep"].structural.Properties["status"].Properties["state"]
	require.NotNil(t, s.ValueValidation)
	var got []string
	for _, e := range s.ValueValidation.Enum {
		got = append(got, fmt.Sprint(e.Object))
	}
	want := []string{
		"Pending", "Promoting", "WaitingForMerge", "HealthChecking", "Verifying", "Verified",
		"Failed", "AbortedByAlarm", "RollingBack",
	}
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

// TestCRDShortNamesDoNotShadowBuiltins: kubectl resolves a short name to the
// built-in resource first, so a colliding short name is unusable.
func TestCRDShortNamesDoNotShadowBuiltins(t *testing.T) {
	builtin := map[string]bool{
		"cm": true, "cs": true, "csr": true, "crd": true, "crds": true, "deploy": true,
		"ds": true, "ep": true, "ev": true, "hpa": true, "ing": true, "limits": true,
		"netpol": true, "no": true, "ns": true, "pc": true, "pdb": true, "po": true,
		"pv": true, "pvc": true, "quota": true, "rc": true, "rs": true, "sa": true,
		"sc": true, "sts": true, "svc": true,
	}
	for kind, c := range loadCRDs(t) {
		for _, sn := range c.crd.Spec.Names.ShortNames {
			assert.False(t, builtin[sn], "%s shortName %q is a built-in kubectl short name", kind, sn)
		}
	}
}

// ── PolicyGate names fit the gate-template label ─────────────────────────────

// TestCRDSchemaPolicyGateName: a PolicyGate name longer than 63 characters is
// refused, because the Graph copies a template's name into the
// kardinal.io/gate-template label of each instance. Only a gate with
// spec.generated, which kardinal sets on the gate instances and freeze gates
// it creates and never uses as a template, may be longer. The names kardinal
// gives those gates ("--" in an instance name, "freeze-<pipeline>") do not
// exempt a gate without it (GATE-REJECT-02). Dots are allowed: a dotted name
// of at most 63 characters is a valid label value.
func TestCRDSchemaPolicyGateName(t *testing.T) {
	crds := loadCRDs(t)
	const instance = "no-weekend-deploys-platform-policies-prod--kardinal-test-app-sha-abc1234"
	freeze := "freeze-" + strings.Repeat("p", 63)
	cases := []struct {
		name      string
		generated bool
		allow     bool
	}{
		{"no-weekend-deploys", false, true},
		{strings.Repeat("g", 63), false, true},
		{"release.v1.2-window", false, true},
		{strings.Repeat("g", 64), false, false},
		{"no-weekend-deploys-for-the-payments-platform-team-in-every-region", false, false},
		// Templates named like kardinal's own gates.
		{"team--" + strings.Repeat("g", 60), false, false},
		{"freeze-" + strings.Repeat("g", 60), false, false},
		{instance, false, false},
		{freeze, false, false},
		// Gate instance <gate>-<namespace>-<env>--<bundle> and the freeze gate of
		// a pipeline with a 63-character name, as kardinal creates them.
		{instance, true, true},
		{freeze, true, true},
		{strings.Repeat("g", 63), true, true},
	}
	for _, c := range cases {
		obj := baseObject("PolicyGate")
		obj["metadata"].(map[string]interface{})["name"] = c.name
		if c.generated {
			obj["spec"].(map[string]interface{})["generated"] = true
		}
		errs := validateCR(t, crds, obj)
		if c.allow {
			assert.Empty(t, errs, "%s generated=%v", c.name, c.generated)
		} else {
			require.Len(t, errs, 1, "%s generated=%v", c.name, c.generated)
			assert.Contains(t, errs[0], "at most 63 characters", c.name)
		}
	}
}

// TestCRDPolicyGateNameRuleOnUpdate runs the name rule through the API
// server's validator. A long-named gate kardinal created before
// spec.generated existed is refused on its next status write, so the
// PolicyGate reconciler sets spec.generated on it first; ratcheting does not
// help, because the rule is on the root and the root changes. A PolicyGate
// with no spec at all is judged by its name only.
func TestCRDPolicyGateNameRuleOnUpdate(t *testing.T) {
	crds := loadCRDs(t)
	const name = "no-weekend-deploys-platform-policies-prod--kardinal-test-app-sha-abc1234"
	gate := func(generated bool, reason string) map[string]interface{} {
		obj := baseObject("PolicyGate")
		obj["metadata"].(map[string]interface{})["name"] = name
		if generated {
			obj["spec"].(map[string]interface{})["generated"] = true
		}
		if reason != "" {
			obj["status"] = map[string]interface{}{"ready": false, "reason": reason}
		}
		return obj
	}
	for _, ratchet := range []bool{false, true} {
		assertRejectedWith(t, celAdmission(t, crds, gate(false, "blocked"), gate(false, ""), ratchet),
			"at most 63 characters")
		assert.Empty(t, celAdmission(t, crds, gate(true, ""), gate(false, ""), ratchet), "marking it")
		assert.Empty(t, celAdmission(t, crds, gate(true, "blocked"), gate(true, ""), ratchet), "status write once marked")
	}

	noSpec := baseObject("PolicyGate")
	delete(noSpec, "spec")
	noSpec["metadata"].(map[string]interface{})["name"] = name
	assertRejectedWith(t, celAdmission(t, crds, noSpec, nil, false), "at most 63 characters")
	noSpec["metadata"].(map[string]interface{})["name"] = "short"
	assert.Empty(t, celAdmission(t, crds, noSpec, nil, false))
}

// TestCRDSchemaAcceptsShippedPolicyGates: every PolicyGate in examples/, demo/
// and config/samples passes the PolicyGate CRD, name rule included.
func TestCRDSchemaAcceptsShippedPolicyGates(t *testing.T) {
	crds := loadCRDs(t)
	checked := 0
	for _, dir := range []string{"examples", "demo", filepath.Join("config", "samples")} {
		require.NoError(t, filepath.Walk(filepath.Join(repoRootDir(t), dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || (!strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml")) {
				return err
			}
			for _, doc := range yamlDocuments(t, p) {
				if !bytes.Contains(doc, []byte("kind: PolicyGate")) {
					continue
				}
				obj := toUnstructured(t, doc)
				if obj == nil || obj["kind"] != "PolicyGate" {
					continue
				}
				checked++
				assert.Empty(t, validateCR(t, crds, obj), "%s: %v", p, obj["metadata"])
			}
			return nil
		}))
	}
	assert.Greater(t, checked, 10)
}

// TestCRDPipelineGitBranchDefault (B99): the Pipeline CRD defaults
// spec.git.branch to main. Without the default, a Pipeline that omits the
// field sent an empty PR base and the SCM refused every pr-review PR. The API
// server applies a structural default on create and update and when it reads
// an object from etcd, so Pipelines created before the default get it too. It
// fills only an absent (or null) field: an explicit "" stays "", which is why
// the controller also treats "" as main.
func TestCRDPipelineGitBranchDefault(t *testing.T) {
	s := loadCRDs(t)["Pipeline"].structural
	branch := s.Properties["spec"].Properties["git"].Properties["branch"]
	assert.Equal(t, "main", branch.Default.Object, "spec.git.branch default")

	doc := func(git string) map[string]interface{} {
		return toUnstructured(t, []byte(`apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: {name: p, namespace: default}
spec:
  git: {url: "https://example.com/org/repo.git"`+git+`}
  environments: [{name: prod, approval: pr-review}]
`))
	}
	gitBranch := func(obj map[string]interface{}) interface{} {
		return obj["spec"].(map[string]interface{})["git"].(map[string]interface{})["branch"]
	}
	for _, tc := range []struct {
		name, git string
		want      interface{}
	}{
		{"omitted", "", "main"},
		{"null", ", branch: null", "main"},
		{"set", ", branch: release", "release"},
		{"empty string is kept", `, branch: ""`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := doc(tc.git)
			structuraldefaulting.Default(obj, s)
			assert.Equal(t, tc.want, gitBranch(obj))
		})
	}
}

// TestCRDYAMLUpdateFile (#1448 QA): update.yaml.updates[].file is a path
// inside the environment directory.
func TestCRDYAMLUpdateFile(t *testing.T) {
	crds := loadCRDs(t)
	withFile := func(file string) map[string]interface{} {
		p := pipelineWithEnvs("test")
		env := p["spec"].(map[string]interface{})["environments"].([]interface{})[0].(map[string]interface{})
		env["update"] = map[string]interface{}{"strategy": "yaml", "yaml": map[string]interface{}{
			"updates": []interface{}{map[string]interface{}{"file": file, "path": "image.tag"}}}}
		return p
	}
	for _, ok := range []string{"values.yaml", "deploy/deployment.yaml", "_x.yml"} {
		assert.Empty(t, validateCR(t, crds, withFile(ok)), "file %q must be accepted", ok)
	}
	for _, bad := range []string{"/etc/passwd", "../other/values.yaml", "deploy/../../x.yaml", ".hidden/x.yaml", "a b.yaml"} {
		assert.NotEmpty(t, validateCR(t, crds, withFile(bad)), "file %q must be rejected", bad)
	}
}

// ── MetricCheck providers (#1445, #1446) ─────────────────────────────────────

// TestCRDMetricCheckProviders: each provider needs its block (and query, but
// web), credentials are Secret refs, threshold.text only takes eq/ne, and a
// v0.9 Prometheus MetricCheck is still valid.
func TestCRDMetricCheckProviders(t *testing.T) {
	crds := loadCRDs(t)
	ref := map[string]interface{}{"name": "s", "key": "k"}
	mc := func(spec map[string]interface{}) map[string]interface{} {
		if _, ok := spec["threshold"]; !ok {
			spec["threshold"] = map[string]interface{}{"operator": "lt", "value": int64(1)}
		}
		return map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "MetricCheck",
			"metadata": map[string]interface{}{"name": "m", "namespace": "default"}, "spec": spec}
	}
	accepted := map[string]map[string]interface{}{
		"v0.9 prometheus": mc(map[string]interface{}{"provider": "prometheus", "prometheusURL": "http://p:9090", "query": "up",
			"threshold": map[string]interface{}{"operator": "gte", "value": int64(1)}}),
		"prometheus with auth": mc(map[string]interface{}{"provider": "prometheus", "prometheusURL": "http://p:9090", "query": "up",
			"prometheus": map[string]interface{}{"authorizationSecretRef": ref}}),
		"datadog": mc(map[string]interface{}{"provider": "datadog", "query": "avg:x{*}",
			"datadog": map[string]interface{}{"site": "datadoghq.eu", "apiKeySecretRef": ref, "applicationKeySecretRef": ref}}),
		"cloudwatch with keys": mc(map[string]interface{}{"provider": "cloudwatch", "query": "SELECT 1",
			"cloudWatch": map[string]interface{}{"region": "us-gov-west-1", "accessKeyIDSecretRef": ref, "secretAccessKeySecretRef": ref, "sessionTokenSecretRef": ref}}),
		"cloudwatch ambient": mc(map[string]interface{}{"provider": "cloudwatch", "query": "SELECT 1",
			"cloudWatch": map[string]interface{}{"region": "eu-west-1"}}),
		"newrelic": mc(map[string]interface{}{"provider": "newrelic", "query": "SELECT count(*) FROM T",
			"newRelic": map[string]interface{}{"accountID": int64(1), "region": "EU", "apiKeySecretRef": ref}}),
		"web text": mc(map[string]interface{}{"provider": "web", "perPromotion": true,
			"web": map[string]interface{}{"url": "https://x/{{ bundle.version }}", "jsonPath": "{.status}",
				"headers": []interface{}{map[string]interface{}{"name": "Authorization", "valueFromSecret": ref},
					map[string]interface{}{"name": "X-Env", "value": "prod"}}},
			"threshold": map[string]interface{}{"operator": "eq", "text": "ok"}}),
	}
	for name, obj := range accepted {
		assert.Empty(t, validateCR(t, crds, obj), "%s must be accepted", name)
	}
	rejected := map[string]map[string]interface{}{
		"prometheus without URL": mc(map[string]interface{}{"provider": "prometheus", "query": "up"}),
		"datadog without block":  mc(map[string]interface{}{"provider": "datadog", "query": "q"}),
		"datadog without keys":   mc(map[string]interface{}{"provider": "datadog", "query": "q", "datadog": map[string]interface{}{}}),
		"cloudwatch half keys": mc(map[string]interface{}{"provider": "cloudwatch", "query": "q",
			"cloudWatch": map[string]interface{}{"region": "eu-west-1", "accessKeyIDSecretRef": ref}}),
		"cloudwatch token only": mc(map[string]interface{}{"provider": "cloudwatch", "query": "q",
			"cloudWatch": map[string]interface{}{"region": "eu-west-1", "sessionTokenSecretRef": ref}}),
		"cloudwatch bad region": mc(map[string]interface{}{"provider": "cloudwatch", "query": "q",
			"cloudWatch": map[string]interface{}{"region": "https://evil"}}),
		"newrelic without query": mc(map[string]interface{}{"provider": "newrelic",
			"newRelic": map[string]interface{}{"accountID": int64(1), "apiKeySecretRef": ref}}),
		"web without block": mc(map[string]interface{}{"provider": "web"}),
		"web header both": mc(map[string]interface{}{"provider": "web", "web": map[string]interface{}{"url": "http://x", "jsonPath": "{.a}",
			"headers": []interface{}{map[string]interface{}{"name": "A", "value": "v", "valueFromSecret": ref}}}}),
		"web jsonPath without braces": mc(map[string]interface{}{"provider": "web",
			"web": map[string]interface{}{"url": "http://x", "jsonPath": ".a"}}),
		"text with lt": mc(map[string]interface{}{"provider": "web", "web": map[string]interface{}{"url": "http://x", "jsonPath": "{.a}"},
			"threshold": map[string]interface{}{"operator": "lt", "text": "x"}}),
		"unknown provider": mc(map[string]interface{}{"provider": "graphite", "query": "q"}),
		"web recursive descent": mc(map[string]interface{}{"provider": "web",
			"web": map[string]interface{}{"url": "http://x", "jsonPath": "{..a}"}}),
	}
	for name, obj := range rejected {
		assert.NotEmpty(t, validateCR(t, crds, obj), "%s must be rejected", name)
	}
}

// TestCRDBundleImageTag: a Bundle image tag follows the OCI grammar, so a
// per-promotion MetricCheck never gets an empty, dotted or quoted tag
// (QA #1479).
func TestCRDBundleImageTag(t *testing.T) {
	crds := loadCRDs(t)
	bundle := func(tag string) map[string]interface{} {
		return map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
			"metadata": map[string]interface{}{"name": "b", "namespace": "default"},
			"spec": map[string]interface{}{"type": "image", "pipeline": "p",
				"images": []interface{}{map[string]interface{}{"repository": "ghcr.io/a/b", "tag": tag}}}}
	}
	for _, ok := range []string{"1.2.3", "sha-abc1234", "v1_rc.2", "latest", "6.14.1-alpine"} {
		assert.Empty(t, validateCR(t, crds, bundle(ok)), "tag %q must be accepted", ok)
	}
	for _, bad := range []string{".hidden", "-x", `x"} or vector(1)`, "a b", strings.Repeat("a", 129)} {
		assert.NotEmpty(t, validateCR(t, crds, bundle(bad)), "tag %q must be rejected", bad)
	}
}

// TestCRDBundleDigestAndCommit (QA #1479): digests follow the OCI digest
// grammar and commit SHAs are hex (provenance also takes a digest, which a
// Subscription records), so a placeholder never gets "@", a quote or a
// space from them.
func TestCRDBundleDigestAndCommit(t *testing.T) {
	crds := loadCRDs(t)
	bundle := func(digest, prov, config string) map[string]interface{} {
		spec := map[string]interface{}{"type": "mixed", "pipeline": "p",
			"images":     []interface{}{map[string]interface{}{"repository": "ghcr.io/a/b", "tag": "1", "digest": digest}},
			"provenance": map[string]interface{}{"commitSHA": prov},
			"configRef":  map[string]interface{}{"commitSHA": config}}
		return map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
			"metadata": map[string]interface{}{"name": "b", "namespace": "default"}, "spec": spec}
	}
	d := "sha256:" + strings.Repeat("a", 64)
	assert.Empty(t, validateCR(t, crds, bundle(d, "abc1234", "0123456789abcdef0123456789abcdef01234567")))
	assert.Empty(t, validateCR(t, crds, bundle(d, d, "abcd")), "provenance may hold a digest")
	for name, b := range map[string]map[string]interface{}{
		"digest with @":        bundle("sha256:x@evil", "abc1234", "abcd"),
		"short digest":         bundle("sha256:abc", "abc1234", "abcd"),
		"commit with a space":  bundle(d, "abc 123", "abcd"),
		"commit with @":        bundle(d, "abc@evil.example", "abcd"),
		"configRef not hex":    bundle(d, "abc1234", "main"),
		"configRef with colon": bundle(d, "abc1234", d),
	} {
		assert.NotEmpty(t, validateCR(t, crds, b), "%s must be rejected", name)
	}
}
