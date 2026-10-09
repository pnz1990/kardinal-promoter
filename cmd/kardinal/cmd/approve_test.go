// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApprove_IsDeprecatedNoOp covers C09a-cli-03 / E2E-19: approve never
// bypassed anything, so it now fails with a pointer to override and leaves
// the Bundle untouched.
func TestApprove_IsDeprecatedNoOp(t *testing.T) {
	for _, args := range [][]string{{"nginx-demo-v1-29-0", "--env", "prod"}, {"nginx-demo-v1-29-0"}} {
		cmd := newApproveCmd()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		require.ErrorIs(t, err, errApproveRemoved)
		assert.Contains(t, err.Error(), "kardinal override")
		assert.NotEmpty(t, cmd.Deprecated)
	}
}

// --- #406: pause/resume lifecycle tests ---
