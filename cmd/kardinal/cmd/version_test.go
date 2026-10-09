// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVersionOutput_ThreeLines verifies that versionFn outputs CLI, Controller, and Graph lines.
func TestVersionOutput_ThreeLines(t *testing.T) {
	tests := []struct {
		name        string
		controllerV string
		graphV      string
		wantLines   []string
	}{
		{
			name:        "all versions known",
			controllerV: "v0.2.0",
			graphV:      "v0.9.1",
			wantLines:   []string{"CLI:", "Controller: v0.2.0", "Graph:      v0.9.1"},
		},
		{
			name:        "graph version unknown",
			controllerV: "v0.2.0",
			graphV:      "",
			wantLines:   []string{"CLI:", "Controller: v0.2.0", "Graph:      (unknown)"},
		},
		{
			name:        "controller version unknown",
			controllerV: "",
			graphV:      "",
			wantLines:   []string{"CLI:", "Controller: unknown", "Graph:      (unknown)"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := versionFn(&buf, tc.controllerV, tc.graphV)
			require.NoError(t, err)
			out := buf.String()
			for _, want := range tc.wantLines {
				assert.Contains(t, out, want, "output should contain %q", want)
			}
		})
	}
}
