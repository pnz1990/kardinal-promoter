// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// TestChartNamePattern: Bundle spec.chart.name and Subscription
// spec.helm.chart accept Helm chart names only. The name is joined into the
// chart index and OCI paths, so "/", "]", "..", "?", "#", "%" or whitespace
// could point the version lookup at another chart (QA on #1478). The
// patterns are the generated CRDs', and the chart ships the same files.
func TestChartNamePattern(t *testing.T) {
	fields := []struct {
		file string
		path []string
	}{
		{"kardinal.io_bundles.yaml", []string{"spec", "chart", "name"}},
		{"kardinal.io_subscriptions.yaml", []string{"spec", "helm", "chart"}},
	}
	var pattern string
	for _, f := range fields {
		for _, dir := range []string{"../../config/crd/bases/", "../../chart/kardinal-promoter/crds/"} {
			raw, err := os.ReadFile(dir + f.file)
			require.NoError(t, err)
			var crd apiextensionsv1.CustomResourceDefinition
			require.NoError(t, yaml.Unmarshal(raw, &crd))
			node := *crd.Spec.Versions[0].Schema.OpenAPIV3Schema
			for _, p := range f.path {
				node = node.Properties[p]
			}
			require.NotEmpty(t, node.Pattern, "%s%s %v", dir, f.file, f.path)
			if pattern == "" {
				pattern = node.Pattern
			}
			assert.Equal(t, pattern, node.Pattern, "%s%s %v has the same rule", dir, f.file, f.path)
			require.NotNil(t, node.MaxLength)
		}
	}
	re := regexp.MustCompile(pattern)
	for name, want := range map[string]bool{
		"podinfo":            true,
		"kube-prometheus":    true,
		"my_chart.v2":        true,
		"Chart1":             true,
		"a":                  true,
		"":                   false,
		"-podinfo":           false,
		"podinfo-":           false,
		".hidden":            false,
		"../other":           false,
		"org/podinfo":        false,
		"podinfo]":           false,
		"podinfo]/../x":      false,
		"pod info":           false,
		"podinfo?version=1":  false,
		"podinfo#x":          false,
		"podinfo%2f..":       false,
		"podinfo\n":          false,
		`podinfo\..\other`:   false,
		"podinfo:1.0.0":      false,
		"podinfo@sha256:abc": false,
	} {
		assert.Equal(t, want, re.MatchString(name), "%q", name)
	}
}
