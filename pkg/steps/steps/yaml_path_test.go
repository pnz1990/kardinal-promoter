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
		"images[name=ghcr.io/org/app:v1@sha].tag":       "images[name=ghcr.io/org/app:v1@sha].tag",
		"a_b-c.0d": "a_b-c.0d",
	}
	for in, want := range ok {
		segs, err := parseYAMLPath(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, pathString(segs, len(segs)), in)
	}
	for _, bad := range []string{"", ".", "a..b", "a.", "[0]", "a[", "a[]", "a[-1]", "a[01]", "a[x]", "a[=v]",
		"a[f=]", "a[f=v w]", "a b", "a.b[0]c", "a/b", "a[f=v]x", "a.*"} {
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
		"a b", "a.b[0]c", "a/b", "a[f=v]x", "a.*", "a[f=v=w]", "a[f=v]]",
	} {
		_, err := parseYAMLPath(p)
		assert.Equal(t, err == nil, re.MatchString(p), "%q: parser ok=%v, CRD pattern ok=%v", p, err == nil, re.MatchString(p))
	}
}
