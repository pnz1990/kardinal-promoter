// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"math"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoMemoryLimit(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	tests := []struct {
		name    string
		env     map[string]string
		want    int64
		wantErr bool
	}{
		{"1Gi limit", map[string]string{envMemoryLimit: "1073741824"}, 966367641, false},
		{"256Mi limit", map[string]string{envMemoryLimit: "268435456"}, 241591910, false},
		{"GOMEMLIMIT wins", map[string]string{envMemoryLimit: "1073741824", "GOMEMLIMIT": "500MiB"}, 0, false},
		{"no limit", map[string]string{}, 0, false},
		{"not a number", map[string]string{envMemoryLimit: "1Gi"}, 0, true},
		{"zero", map[string]string{envMemoryLimit: "0"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, why, err := goMemoryLimit(env(tt.env))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.NotEmpty(t, why)
		})
	}
}

func TestApplyGoMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
	got, _, err := applyGoMemoryLimit(func(k string) string {
		if k == envMemoryLimit {
			return "1073741824"
		}
		return ""
	})
	require.NoError(t, err)
	assert.Equal(t, int64(966367641), got)
	assert.Equal(t, got, debug.SetMemoryLimit(-1), "the runtime's soft limit")

	debug.SetMemoryLimit(math.MaxInt64)
	got, _, err = applyGoMemoryLimit(func(string) string { return "" })
	require.NoError(t, err)
	assert.Zero(t, got)
	assert.Equal(t, int64(math.MaxInt64), debug.SetMemoryLimit(-1), "nothing set without a limit")
}
