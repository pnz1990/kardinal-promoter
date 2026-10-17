// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// userKinds are the kinds a user writes (or CI creates, Bundle):
// kubectl get kardinal lists them. Every kind, generated ones included, is in
// kardinal-all.
var userKinds = []string{"AgentIdentity", "Bundle", "ChangeRequest", "ChangeWindow", "ClusterScmProvider", "MetricCheck",
	"NotificationHook", "Pipeline", "PolicyGate", "ScmProvider", "StepTemplate", "Subscription"}

// TestCRDCategories (P1): kubectl get kardinal lists the kinds users write,
// Pipelines, PolicyGates and Bundles among them, and kubectl get kardinal-all
// every kardinal kind.
//
// Covers PIPE-ENVSTATUS-02.
func TestCRDCategories(t *testing.T) {
	files, err := filepath.Glob("../../config/crd/bases/*.yaml")
	require.NoError(t, err)
	require.Len(t, files, 25)
	var user []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var crd apiextensionsv1.CustomResourceDefinition
		require.NoError(t, yaml.Unmarshal(raw, &crd))
		cats := crd.Spec.Names.Categories
		assert.Contains(t, cats, "kardinal-all", f)
		if slices.Contains(cats, "kardinal") {
			user = append(user, crd.Spec.Names.Kind)
		}
	}
	slices.Sort(user)
	assert.Equal(t, userKinds, user)
}

// TestPrinterColumns (P1): kubectl get pipelines shows the summary first and
// the per-environment versions with -o wide; kubectl get bundles shows the
// version.
//
// Covers PIPE-ENVSTATUS-02.
func TestPrinterColumns(t *testing.T) {
	columns := func(file string) map[string]apiextensionsv1.CustomResourceColumnDefinition {
		raw, err := os.ReadFile("../../config/crd/bases/" + file)
		require.NoError(t, err)
		var crd apiextensionsv1.CustomResourceDefinition
		require.NoError(t, yaml.Unmarshal(raw, &crd))
		out := map[string]apiextensionsv1.CustomResourceColumnDefinition{}
		for i, c := range crd.Spec.Versions[0].AdditionalPrinterColumns {
			if i == 0 {
				out["first"] = c
			}
			out[c.Name] = c
		}
		return out
	}
	p := columns("kardinal.io_pipelines.yaml")
	assert.Equal(t, "Summary", p["first"].Name)
	assert.Equal(t, ".status.summary", p["Summary"].JSONPath)
	assert.Equal(t, int32(0), p["Summary"].Priority)
	assert.Equal(t, ".status.environmentSummary", p["Envs"].JSONPath)
	assert.Equal(t, int32(1), p["Envs"].Priority, "Envs is a -o wide column")
	assert.Equal(t, ".status.version", columns("kardinal.io_bundles.yaml")["Version"].JSONPath)
}
