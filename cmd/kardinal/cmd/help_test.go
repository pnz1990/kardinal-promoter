// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leaves returns every runnable, non-deprecated, non-hidden command below
// cmd.
func leaves(cmd *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Deprecated != "" || c.Name() == "help" {
			continue
		}
		if c.HasSubCommands() {
			out = append(out, leaves(c)...)
			continue
		}
		out = append(out, c)
	}
	return out
}

// TestEveryLeafHasExample (C1): every command a user runs shows at least
// one example in --help, and keeps its Long to 5 lines or fewer (the detail
// is in its reference page, ReferenceAnnotation).
//
// Covers CLI-MODEL-01.
func TestEveryLeafHasExample(t *testing.T) {
	for _, c := range leaves(NewRootCmd()) {
		assert.NotEmpty(t, strings.TrimSpace(c.Example), "%s has no Example", c.CommandPath())
		if n := strings.Count(strings.TrimSpace(c.Long), "\n") + 1; c.Long != "" {
			assert.LessOrEqual(t, n, 5, "%s: Long is %d lines; move the detail to ReferenceAnnotation", c.CommandPath(), n)
		}
	}
}

// updateGolden rewrites the golden files: go test ./cmd/kardinal/cmd -run Golden -update
var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/")

// TestHelp_Golden (C1): kardinal --help, grouped as the command model has it,
// matches testdata/help.golden.
//
// Covers CLI-MODEL-01.
func TestHelp_Golden(t *testing.T) {
	out, err := executeRoot(t, "--help")
	require.NoError(t, err)
	path := filepath.Join("testdata", "help.golden")
	if *updateGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run: go test ./cmd/kardinal/cmd -run TestHelp_Golden -update")
	assert.Equal(t, string(want), out)
}
