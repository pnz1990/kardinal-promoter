// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package examples checks the manifests and READMEs under examples/, demo/ and
// test/pdca/ (the PDCA workflow's fixtures) without a cluster: every kardinal.io object must be accepted by the CRD
// schemas as written, every documented CLI invocation must parse, and every
// path an example points at must exist in the repository it names.
package examples

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apivalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// manifestDirs are the trees whose YAML files are checked.
var manifestDirs = []string{"examples", "demo", "test/pdca"}

// doc is one YAML document from a manifest file or a rendered chart.
type doc struct {
	source string // repo-relative file, or "helm template <chart> -f <values>"
	index  int
	obj    map[string]interface{}
}

func (d doc) String() string {
	return fmt.Sprintf("%s#%d %s/%s", d.source, d.index, str(d.obj, "kind"), str(d.obj, "metadata", "name"))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// str returns the string at the given map path, or "".
func str(obj map[string]interface{}, keys ...string) string {
	v, _ := get(obj, keys...).(string)
	return v
}

func get(obj interface{}, keys ...string) interface{} {
	cur := obj
	for _, k := range keys {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func list(v interface{}) []map[string]interface{} {
	items, _ := v.([]interface{})
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func splitDocs(t *testing.T, source string, raw []byte) []doc {
	t.Helper()
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var out []doc
	for i := 1; ; i++ {
		b, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err, "%s: split YAML", source)
		var obj map[string]interface{}
		require.NoError(t, sigyaml.Unmarshal(b, &obj), "%s#%d: parse YAML", source, i)
		if obj != nil {
			out = append(out, doc{source: source, index: i, obj: obj})
		}
	}
}

// manifestFiles returns the repo-relative YAML files under manifestDirs.
// Helm chart templates are not YAML until rendered, so templates/ is skipped;
// chartRenders covers them.
func manifestFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var files []string
	for _, dir := range manifestDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "templates" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") {
				rel, relErr := filepath.Rel(root, p)
				if relErr != nil {
					return fmt.Errorf("relative path of %s: %w", p, relErr)
				}
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		require.NoError(t, err)
	}
	sort.Strings(files)
	return files
}

// chartRender is one `helm template` invocation of an example chart.
type chartRender struct {
	chart, values, namespace string
}

// chartRenders lists the example charts with each values file that ships
// with them. TestEveryExampleChartIsRendered keeps it complete.
func chartRenders(t *testing.T) []chartRender {
	t.Helper()
	renders := []chartRender{{chart: "examples/multi-tenant/chart", namespace: "team-a"}}
	teams, err := filepath.Glob(filepath.Join(repoRoot(t), "examples/multi-tenant/teams/*/pipeline-values.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, teams, "no team values files under examples/multi-tenant/teams")
	for _, v := range teams {
		team := filepath.Base(filepath.Dir(v))
		renders = append(renders, chartRender{
			chart:     "examples/multi-tenant/chart",
			values:    "examples/multi-tenant/teams/" + team + "/pipeline-values.yaml",
			namespace: team,
		})
	}
	return renders
}

// renderedDocs renders every example chart. Without helm on PATH it skips the
// charts locally and fails in CI, where the workflow installs helm.
func renderedDocs(t *testing.T) []doc {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatal("helm is not on PATH; the example charts cannot be rendered")
		}
		t.Log("helm is not on PATH; skipping the example charts")
		return nil
	}
	var out []doc
	for _, r := range chartRenders(t) {
		args := []string{"template", r.namespace, r.chart, "--namespace", r.namespace}
		source := "helm template " + r.chart
		if r.values != "" {
			args = append(args, "--values", r.values)
			source += " --values " + r.values
		}
		cmd := exec.Command(helm, args...)
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+os.DevNull)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		rendered, err := cmd.Output()
		require.NoError(t, err, "%s: %s", source, stderr.String())
		out = append(out, splitDocs(t, source, rendered)...)
	}
	return out
}

// allDocs returns every document under manifestDirs plus the rendered charts.
func allDocs(t *testing.T) []doc {
	t.Helper()
	var out []doc
	for _, f := range manifestFiles(t) {
		raw, err := os.ReadFile(filepath.Join(repoRoot(t), f))
		require.NoError(t, err)
		out = append(out, splitDocs(t, f, raw)...)
	}
	return append(out, renderedDocs(t)...)
}

func TestEveryExampleChartIsRendered(t *testing.T) {
	root := repoRoot(t)
	rendered := map[string]bool{}
	for _, r := range chartRenders(t) {
		rendered[r.chart] = true
	}
	for _, dir := range manifestDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.Name() != "Chart.yaml" {
				return err
			}
			rel, relErr := filepath.Rel(root, filepath.Dir(p))
			if relErr != nil {
				return fmt.Errorf("relative path of %s: %w", p, relErr)
			}
			assert.True(t, rendered[filepath.ToSlash(rel)], "chart %s is missing from chartRenders", rel)
			return nil
		})
		require.NoError(t, err)
	}
}

type crdSchema struct {
	structural *structuralschema.Structural
	validator  apivalidation.SchemaValidator
}

// loadCRDSchemas reads the v1alpha1 schemas from config/crd/bases, keyed by kind.
func loadCRDSchemas(t *testing.T) map[string]crdSchema {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "config/crd/bases/*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	out := map[string]crdSchema{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var crd apiextv1.CustomResourceDefinition
		require.NoError(t, sigyaml.Unmarshal(raw, &crd), f)
		for _, ver := range crd.Spec.Versions {
			if ver.Name != v1alpha1.GroupVersion.Version || ver.Schema == nil {
				continue
			}
			internal := &apiext.JSONSchemaProps{}
			require.NoError(t, apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
				ver.Schema.OpenAPIV3Schema, internal, nil), f)
			s, err := structuralschema.NewStructural(internal)
			require.NoError(t, err, f)
			val, _, err := apivalidation.NewSchemaValidator(internal)
			require.NoError(t, err, f)
			out[crd.Spec.Names.Kind] = crdSchema{structural: s, validator: val}
		}
	}
	return out
}

// schemaProblems returns what the API server would do to obj beyond accepting
// it: fields it would drop, and validation errors it would reject it with.
func schemaProblems(t *testing.T, s crdSchema, obj map[string]interface{}) []string {
	t.Helper()
	b, err := sigyaml.Marshal(obj)
	require.NoError(t, err)
	var cp map[string]interface{}
	require.NoError(t, sigyaml.Unmarshal(b, &cp))

	var problems []string
	pruned := pruning.PruneWithOptions(cp, s.structural, true,
		structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true})
	for _, p := range pruned {
		problems = append(problems, "unknown field, dropped by the API server: "+p)
	}
	for _, e := range apivalidation.ValidateCustomResource(field.NewPath(""), cp, s.validator) {
		problems = append(problems, "rejected by the CRD schema: "+e.Error())
	}
	return problems
}

func labelProblems(obj map[string]interface{}) []string {
	labels, _ := get(obj, "metadata", "labels").(map[string]interface{})
	var problems []string
	for k, v := range labels {
		sv := fmt.Sprint(v)
		for _, msg := range validation.IsQualifiedName(k) {
			problems = append(problems, fmt.Sprintf("label key %q: %s", k, msg))
		}
		for _, msg := range validation.IsValidLabelValue(sv) {
			problems = append(problems, fmt.Sprintf("label %s=%q: %s", k, sv, msg))
		}
	}
	sort.Strings(problems)
	return problems
}

// TestExampleManifestsMatchCRDSchemas applies the API server's pruning and
// schema validation to every kardinal.io object under examples/ and demo/.
// An unknown field fails the test: the API server would silently drop it, so
// the setting the example shows would have no effect.
func TestExampleManifestsMatchCRDSchemas(t *testing.T) {
	schemas := loadCRDSchemas(t)
	var checked int
	for _, d := range allDocs(t) {
		if !strings.HasPrefix(str(d.obj, "apiVersion"), v1alpha1.GroupVersion.Group+"/") {
			continue
		}
		checked++
		t.Run(d.String(), func(t *testing.T) {
			assert.Equal(t, v1alpha1.GroupVersion.String(), str(d.obj, "apiVersion"))
			s, ok := schemas[str(d.obj, "kind")]
			require.True(t, ok, "no CRD for kind %q", str(d.obj, "kind"))
			assert.Empty(t, schemaProblems(t, s, d.obj))
			assert.Empty(t, labelProblems(d.obj))
		})
	}
	assert.Greater(t, checked, 20, "too few kardinal.io documents found; is the walk broken?")
}

func TestSchemaCheckCatchesKnownMistakes(t *testing.T) {
	schemas := loadCRDSchemas(t)
	tests := []struct {
		name string
		kind string
		yaml string
		want string
	}{
		{
			name: "approvalMode is dropped",
			kind: "Pipeline",
			yaml: "apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata: {name: p}\n" +
				"spec:\n  git: {url: https://github.com/o/r}\n  environments:\n  - {name: prod, approvalMode: pr-review}\n",
			want: "spec.environments[0].approvalMode",
		},
		{
			name: "bundle without pipeline and type is rejected",
			kind: "Bundle",
			yaml: "apiVersion: kardinal.io/v1alpha1\nkind: Bundle\nmetadata: {name: b}\nspec:\n  artifacts: {}\n",
			want: "spec.pipeline",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj map[string]interface{}
			require.NoError(t, sigyaml.Unmarshal([]byte(tt.yaml), &obj))
			assert.Contains(t, strings.Join(schemaProblems(t, schemas[tt.kind], obj), "\n"), tt.want)
		})
	}
	assert.NotEmpty(t, labelProblems(map[string]interface{}{"metadata": map[string]interface{}{
		"labels": map[string]interface{}{"kardinal.io/applies-to": "prod-eu,prod-us"},
	}}), "a comma in a label value must be reported")
}
