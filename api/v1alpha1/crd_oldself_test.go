// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestCRDs_NoOldSelfUnderUncorrelatableLists: the API server refuses a CRD
// whose transition rule (one that reads oldSelf) sits under an array that
// is not a map list. The elements of such an array cannot be matched to
// their old values. The PromotionStep CRD failed to install this way when
// status.pendingAuditEvents embedded AuditEventSpec, which carried the
// immutability rule (#1552). Every generated CRD is checked.
func TestCRDs_NoOldSelfUnderUncorrelatableLists(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "config", "crd", "bases")
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var crd map[string]interface{}
		require.NoError(t, yaml.Unmarshal(raw, &crd))
		for _, v := range crd["spec"].(map[string]interface{})["versions"].([]interface{}) {
			schema := v.(map[string]interface{})["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})
			for _, bad := range oldSelfUnderLists(schema, filepath.Base(f), false) {
				assert.Fail(t, "transition rule under an uncorrelatable list", bad)
			}
		}
	}
}

// oldSelfUnderLists returns the paths of the oldSelf rules below node that
// sit under an array without x-kubernetes-list-type: map (inList).
func oldSelfUnderLists(node map[string]interface{}, path string, inList bool) []string {
	var out []string
	if inList {
		rules, _ := node["x-kubernetes-validations"].([]interface{})
		for _, r := range rules {
			if rule, _ := r.(map[string]interface{})["rule"].(string); strings.Contains(rule, "oldSelf") {
				out = append(out, path+": "+rule)
			}
		}
	}
	if props, ok := node["properties"].(map[string]interface{}); ok {
		for name, p := range props {
			out = append(out, oldSelfUnderLists(p.(map[string]interface{}), path+"."+name, inList)...)
		}
	}
	if items, ok := node["items"].(map[string]interface{}); ok {
		listType, _ := node["x-kubernetes-list-type"].(string)
		out = append(out, oldSelfUnderLists(items, path+"[]", inList || listType != "map")...)
	}
	if extra, ok := node["additionalProperties"].(map[string]interface{}); ok {
		out = append(out, oldSelfUnderLists(extra, path+"{}", inList)...)
	}
	return out
}
