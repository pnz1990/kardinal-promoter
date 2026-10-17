// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// TestParseYAMLPath: update.yaml.updates[].path and
// update.helm.chartVersionPath share one grammar: keys separated by ".",
// an optional leading ".", [N] and [field=value] after a key.
func TestParseYAMLPath(t *testing.T) {
	ok := map[string]string{
		"image.tag":                              "image.tag",
		".image.tag":                             "image.tag",
		"spec.template.spec.containers[0].image": "spec.template.spec.containers[0].image",
		"spec.template.spec.containers[name=app].image": "spec.template.spec.containers[name=app].image",
		".dependencies[name=podinfo].version":           "dependencies[name=podinfo].version",
		".dependencies.0.version":                       "dependencies.0.version",
		"matrix[1][2]":                                  "matrix[1][2]",
		"a[123456789]":                                  "a[123456789]",
		"images[name=ghcr.io/org/app:v1@sha].tag":       "images[name=ghcr.io/org/app:v1@sha].tag",
		"a_b-c.0d": "a_b-c.0d",
		// #1932: a quoted key, for a key with "." or "/".
		`metadata.annotations["app.oam.dev/publishVersion"]`: `metadata.annotations["app.oam.dev/publishVersion"]`,
		`.metadata.annotations["app.kubernetes.io/version"]`: `metadata.annotations["app.kubernetes.io/version"]`,
		`["a.b"].c`:            `["a.b"].c`,
		`.["a.b"]`:             `["a.b"]`,
		`a["b.c"][0]["d/e"].f`: `a["b.c"][0]["d/e"].f`,
		`a["8080"]`:            `a["8080"]`,
	}
	for in, want := range ok {
		segs, err := parseYAMLPath(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, pathString(segs, len(segs)), in)
	}
	for _, bad := range []string{"", ".", "a..b", "a.", "[0]", "a[", "a[]", "a[-1]", "a[01]", "a[x]", "a[=v]",
		"a[f=]", "a[f=v w]", "a b", "a.b[0]c", "a/b", "a[f=v]x", "a.*", "a[1234567890]",
		`a[""]`, `a["b`, `a["b]`, `a[b"]`, `a.["b"]`, `a["b c"]`, `a["b\"c"]`, `a["b]"]`, `a['b']`, `a["b"]c`, `a["b"].`} {
		_, err := parseYAMLPath(bad)
		assert.Error(t, err, "%q must be refused", bad)
	}
}

// TestSetYAMLPath_Grammar: the selector and the digits-as-index form work in
// yaml-update paths too, a digits key in a mapping stays a key, and a missing
// list element is an error.
func TestSetYAMLPath_Grammar(t *testing.T) {
	const src = "containers:\n- name: sidecar\n  image: s:1\n- name: app\n  image: a:1\nports:\n  \"8080\": web\n"
	tests := []struct {
		path, want, wantErr string
	}{
		{path: "containers[name=app].image", want: "a:2"},
		{path: ".containers[1].image", want: "a:2"},
		{path: "containers.1.image", want: "a:2"},
		{path: "ports.8080", want: "a:2"},
		{path: "containers[name=web].image", wantErr: "has no element with name \"web\""},
		{path: "containers[5].image", wantErr: "has 2 elements, no [5]"},
		{path: "containers.app.image", wantErr: "is a list; pick an element"},
		{path: "missing[0].image", wantErr: "missing does not exist"},
		// A digits key over a missing value is a list index too: no
		// {"0": ...} mapping is created where a list was meant (QA #1546).
		{path: "missing.0.image", wantErr: "missing does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			doc, err := parseYAMLMapping([]byte(src))
			require.NoError(t, err)
			err = setYAMLPath(doc.root(), tt.path, "a:2")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			got, ok := getYAMLPath(doc.root(), tt.path)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestYAMLPathCRDPatterns: the Pipeline CRD's patterns for both path fields
// are the same, and accept exactly the paths parseYAMLPath accepts, so the
// API server refuses what the step would refuse and nothing else.
//
// Covers STEP-YAML-04.
func TestYAMLPathCRDPatterns(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "crd", "bases", "kardinal.io_pipelines.yaml"))
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	update := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["environments"].Items.Schema.Properties["update"]
	yamlPath := update.Properties["yaml"].Properties["updates"].Items.Schema.Properties["path"].Pattern
	chartPath := update.Properties["helm"].Properties["chartVersionPath"].Pattern
	require.NotEmpty(t, yamlPath)
	assert.Equal(t, yamlPath, chartPath, "one grammar for both fields")
	re := regexp.MustCompile(yamlPath)
	for _, p := range []string{
		"image.tag", ".image.tag", "a[0]", "a[10][2].b", "a[name=app].image", ".dependencies[name=podinfo].version",
		".dependencies.0.version", "images[name=ghcr.io/org/app:v1@sha].tag", "a_b-c.0d",
		"", ".", "a..b", "a.", "[0]", "a[", "a[]", "a[-1]", "a[01]", "a[x]", "a[=v]", "a[f=]", "a[f=v w]",
		"a b", "a.b[0]c", "a/b", "a[f=v]x", "a.*", "a[f=v=w]", "a[f=v]]", "a[123456789]", "a[1234567890]",
		`metadata.annotations["app.oam.dev/publishVersion"]`, `["a.b"].c`, `.["a.b"]`, `a["b.c"][0]["d/e"].f`, `a["8080"]`,
		`a[""]`, `a["b`, `a["b]`, `a[b"]`, `a.["b"]`, `a["b c"]`, `a["b\"c"]`, `a["b]"]`, `a['b']`, `a["b"]c`, `a["b"].`,
	} {
		_, err := parseYAMLPath(p)
		assert.Equal(t, err == nil, re.MatchString(p), "%q: parser ok=%v, CRD pattern ok=%v", p, err == nil, re.MatchString(p))
	}
}

// TestSetYAMLPath_QuotedKeys (#1932): a quoted key addresses a mapping key
// that contains "." or "/" (KubeVela's app.oam.dev/publishVersion, an
// app.kubernetes.io label), is created when missing like any key, is a
// mapping key even when all digits, and keeps the other rules: a scalar
// target, no anchors.
//
// Covers STEP-YAML-04.
func TestSetYAMLPath_QuotedKeys(t *testing.T) {
	const src = "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: web\n  annotations:\n" +
		"    app.oam.dev/publishVersion: \"v1\" # bumped by kardinal\n" +
		"spec:\n  components:\n  - name: web\n    properties:\n      image: app:1\n" +
		"list:\n  - a\nanchored: &x {k: v}\n"
	tests := []struct {
		path, wantErr string
		check         string // another path that must keep its value
	}{
		{path: `metadata.annotations["app.oam.dev/publishVersion"]`},
		{path: `metadata.labels["app.kubernetes.io/version"]`},         // created
		{path: `metadata.annotations["argocd.argoproj.io/sync-wave"]`}, // created next to another
		{path: `["top.level/key"]`},
		{path: `metadata.annotations["8080"]`},
		{path: `metadata["annotations"]["app.oam.dev/publishVersion"]`},
		{path: `metadata.annotations`, wantErr: "is not a scalar"},
		{path: `list["0"]`, wantErr: "is a list; pick an element"},
		{path: `anchored["k"]`, wantErr: "anchor"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			doc, err := parseYAMLMapping([]byte(src))
			require.NoError(t, err)
			err = setYAMLPath(doc.root(), tt.path, "v2")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			got, ok := getYAMLPath(doc.root(), tt.path)
			require.True(t, ok)
			assert.Equal(t, "v2", got)
			img, ok := getYAMLPath(doc.root(), "spec.components[name=web].properties.image")
			require.True(t, ok)
			assert.Equal(t, "app:1", img, "nothing else changes")
		})
	}
	// The comment and quoting of the replaced value are kept.
	doc, err := parseYAMLMapping([]byte(src))
	require.NoError(t, err)
	require.NoError(t, setYAMLPath(doc.root(), `metadata.annotations["app.oam.dev/publishVersion"]`, "v2"))
	out, err := doc.encode()
	require.NoError(t, err)
	assert.Contains(t, string(out), `app.oam.dev/publishVersion: "v2" # bumped by kardinal`)
}

// TestSetYAMLPath_DigitsOverMissingOrNullList (QA #1546): a chart version
// path ".dependencies.0.version" over a Chart.yaml whose dependencies are
// missing or null fails with "does not exist" and leaves the file a valid
// chart, instead of writing dependencies: {"0": {version: ...}}.
func TestSetYAMLPath_DigitsOverMissingOrNullList(t *testing.T) {
	for name, src := range map[string]string{
		"missing dependencies": "apiVersion: v2\nname: umbrella\nversion: 1.0.0\n",
		"null dependencies":    "apiVersion: v2\nname: umbrella\nversion: 1.0.0\ndependencies:\n",
		"null dependencies ~":  "apiVersion: v2\nname: umbrella\nversion: 1.0.0\ndependencies: ~\n",
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := parseYAMLMapping([]byte(src))
			require.NoError(t, err)
			err = setYAMLPath(doc.root(), ".dependencies.0.version", "2.0.0")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "dependencies does not exist")
			out, err := doc.encode()
			require.NoError(t, err)
			assert.NotContains(t, string(out), "\"0\"", "no mapping key 0 written")
			assert.NotContains(t, string(out), "2.0.0")
		})
	}
}
