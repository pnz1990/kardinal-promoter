// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// maxScenarioSeconds bounds a scenario package's recorded time under -race
// on CI: half the go test shards' -timeout 120s, so a slow runner does not
// time a package out. A package over it is split (the matrix runs in parts,
// test/scenario/matrix) or made lighter at its quick size.
const maxScenarioSeconds = 60

// TestScenarioDurations: every package under test/scenario with tests is in
// hack/ci/test-durations.txt (a package not listed counts as the median,
// which hides it), and none is recorded over maxScenarioSeconds; nor is
// test/hack, this package, which reads the whole repository.
func TestScenarioDurations(t *testing.T) {
	root := repoRoot(t)
	const module = "github.com/kardinal-promoter/kardinal-promoter/"
	f, err := os.Open(filepath.Join(root, "hack", "ci", "test-durations.txt"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	recorded := map[string]float64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		secs, err := strconv.ParseFloat(fields[1], 64)
		require.NoError(t, err, sc.Text())
		recorded[strings.TrimPrefix(fields[0], module)] = secs
	}
	require.NoError(t, sc.Err())
	seen := map[string]bool{}
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "test", "scenario"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		if seen[pkg] {
			return nil
		}
		seen[pkg] = true
		secs, ok := recorded[pkg]
		if assert.True(t, ok, "%s is not in hack/ci/test-durations.txt", pkg) {
			assert.LessOrEqual(t, secs, float64(maxScenarioSeconds), "%s is recorded at %.1fs under -race: split it or make its quick size lighter", pkg, secs)
		}
		return nil
	}))
	assert.NotEmpty(t, seen)
	if secs, ok := recorded["test/hack"]; assert.True(t, ok, "test/hack is not in hack/ci/test-durations.txt") {
		assert.LessOrEqual(t, secs, float64(maxScenarioSeconds), "test/hack is recorded at %.1fs under -race", secs)
	}
}
