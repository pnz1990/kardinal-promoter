// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// runYAMLUpdate runs yaml-update setting image.tag to 2.0.0 in a values.yaml
// holding doc, and returns the result and how long it took.
func runYAMLUpdate(t *testing.T, doc string) (parentsteps.StepResult, time.Duration) {
	t.Helper()
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	file := filepath.Join(envDir, "values.yaml")
	require.NoError(t, os.WriteFile(file, []byte(doc), 0o644))
	state := &parentsteps.StepState{
		WorkDir: dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "yaml",
			YAML: &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{{File: "values.yaml", Path: "image.tag"}}}}},
		Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "2.0.0"}}},
		Outputs: map[string]string{},
	}
	start := time.Now()
	res, got := runEditor(t, &yamlUpdateStep{}, state, file, []byte(doc))
	took := time.Since(start)
	if res.Status != parentsteps.StepSuccess {
		require.Equal(t, doc, string(got), "a refused edit leaves the file as it was")
	}
	return res, took
}

// mergedDefaults is an ordinary Helm values file (QA, #2084): a keys-key
// defaults block merged into services entries, no alias of an alias.
func mergedDefaults(keys, services int) string {
	var b strings.Builder
	b.WriteString("image:\n  tag: 1.0.0\ndefaults: &d\n")
	for k := 0; k < keys; k++ {
		fmt.Fprintf(&b, "  key%02d: value-%02d\n", k, k)
	}
	b.WriteString("services:\n")
	for s := 0; s < services; s++ {
		fmt.Fprintf(&b, "  svc%03d:\n    <<: *d\n    name: svc%03d\n", s, s)
	}
	return b.String()
}

// sharedBlock is a keys-key block referenced refs times by alias.
func sharedBlock(keys, refs int) string {
	var b strings.Builder
	b.WriteString("image:\n  tag: 1.0.0\nshared: &s\n")
	for k := 0; k < keys; k++ {
		fmt.Fprintf(&b, "  k%04d: v\n", k)
	}
	b.WriteString("uses:\n")
	for r := 0; r < refs; r++ {
		fmt.Fprintf(&b, "  u%02d: *s\n", r)
	}
	return b.String()
}

// listBomb is levels lists of fan aliases each to the list before it:
// fan^levels values.
func listBomb(fan, levels int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "image:\n  tag: 1.0.0\na0: &a0 [%s]\n", strings.TrimSuffix(strings.Repeat("x, ", fan), ", "))
	for i := 1; i < levels; i++ {
		fmt.Fprintf(&b, "a%d: &a%d [%s]\n", i, i, strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*a%d, ", i-1), fan), ", "))
	}
	return b.String()
}

// mergeBomb is levels mappings of ten keys each merging the mapping before
// it (QA's c<j>: {<<: *m<i-1>}).
func mergeBomb(levels int) string {
	var b strings.Builder
	b.WriteString("image:\n  tag: 1.0.0\nm0: &m0 {a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8, i: 9, j: 10}\n")
	for i := 1; i < levels; i++ {
		fmt.Fprintf(&b, "m%d: &m%d\n", i, i)
		for j := 0; j < 10; j++ {
			fmt.Fprintf(&b, "  c%d: {<<: *m%d}\n", j, i-1)
		}
	}
	return b.String()
}

// fanOut is one anchored block of nodes nodes referenced refs times.
func fanOut(nodes, refs int) string {
	var b strings.Builder
	b.WriteString("image:\n  tag: 1.0.0\nblock: &b [")
	b.WriteString(strings.TrimSuffix(strings.Repeat("x, ", nodes), ", "))
	b.WriteString("]\nrefs: [")
	b.WriteString(strings.TrimSuffix(strings.Repeat("*b, ", refs), ", "))
	b.WriteString("]\n")
	return b.String()
}

// TestYAMLUpdate_AliasBound (#2078, #2084 QA): the bound on alias
// expansion edits ordinary values files whose shared blocks are merged or
// aliased into hundreds of entries, as main does, and refuses alias, merge
// key and fan-out bombs, the message saying why. The bound is pinned both
// ways: a hundredth of it refuses the ordinary file, a hundred times it
// lets a bomb just past it through.
// Covers STEP-FUZZ-03.
func TestYAMLUpdate_AliasBound(t *testing.T) {
	ordinary := map[string]string{
		"40-key defaults merged into 300 services": mergedDefaults(40, 300),
		"1,000-key block aliased 20 times":         sharedBlock(1000, 20),
	}
	for name, doc := range ordinary {
		t.Run("edits: "+name, func(t *testing.T) {
			res, took := runYAMLUpdate(t, doc)
			assert.Equal(t, parentsteps.StepSuccess, res.Status, res.Message)
			t.Logf("%d bytes: %s in %s", len(doc), res.Status, took)
		})
	}
	bombs := map[string]string{
		"aliases of aliases, 10^8 values": aliasBomb(8) + "image: {tag: 1}\n",
		"lists, 10^9 values":              listBomb(10, 9),
		"merge keys, 10^8 values":         mergeBomb(8),
		"a 400-node block, 5,000 times":   fanOut(400, 5000),
	}
	for name, doc := range bombs {
		t.Run("refuses: "+name, func(t *testing.T) {
			res, took := runYAMLUpdate(t, doc)
			assert.Equal(t, parentsteps.StepFailed, res.Status)
			assert.Contains(t, res.Message, errAliasExpansion.Error())
			assert.NotContains(t, res.Message, "nested")
			t.Logf("%d bytes: refused in %s", len(doc), took)
		})
	}

	// Pinned: just past the bound, 4^10 (about 1.05M) values in lists.
	past := listBomb(4, 10)
	res, _ := runYAMLUpdate(t, past)
	require.Equal(t, parentsteps.StepFailed, res.Status, "a bomb just past the bound is refused")
	floor, ratio := aliasFloor, aliasRatio
	t.Cleanup(func() { aliasFloor, aliasRatio = floor, ratio })

	aliasFloor = floor * 100
	res, _ = runYAMLUpdate(t, past)
	assert.Equal(t, parentsteps.StepSuccess, res.Status, "100 times the bound lets it through: the bound is not that high: %s", res.Message)

	aliasFloor, aliasRatio = floor/100, 1
	res, _ = runYAMLUpdate(t, mergedDefaults(40, 300))
	assert.Equal(t, parentsteps.StepFailed, res.Status, "a hundredth of the bound refuses the ordinary file: the bound is not that low")
}
