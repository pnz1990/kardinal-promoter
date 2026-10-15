// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMatrixParts: test/scenario/matrix has a package for each of the
// MatrixParts parts and no other, so every row runs exactly once.
func TestMatrixParts(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(RepoRoot(), "test", "scenario", "matrix"))
	require.NoError(t, err)
	var got []string
	for _, e := range entries {
		if e.IsDir() {
			got = append(got, e.Name())
		}
	}
	var want []string
	for n := 1; n <= MatrixParts; n++ {
		want = append(want, fmt.Sprintf("part%d", n))
	}
	assert.ElementsMatch(t, want, got)
}
