// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletion_Bash(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)

	root.SetArgs([]string{"completion", "bash"})
	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "kardinal", "bash completion output must reference the command name")
}

func TestCompletion_Zsh(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)

	root.SetArgs([]string{"completion", "zsh"})
	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "kardinal", "zsh completion output must reference the command name")
}

func TestCompletion_Fish(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)

	root.SetArgs([]string{"completion", "fish"})
	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "kardinal", "fish completion output must reference the command name")
}

func TestCompletion_PowerShell(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)

	root.SetArgs([]string{"completion", "powershell"})
	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.NotEmpty(t, out, "powershell completion output must not be empty")
}

func TestCompletion_InvalidShell(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"completion", "nushell"})
	err := root.Execute()
	// cobra's OnlyValidArgs guard returns an error for unknown shells
	assert.Error(t, err, "unknown shell must return an error")
}

func TestCompletion_NoArgs(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"completion"})
	err := root.Execute()
	assert.Error(t, err, "missing shell argument must return an error")
}
