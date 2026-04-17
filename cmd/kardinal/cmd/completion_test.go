// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletion_Bash(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "bash"})

	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, len(out) > 0, "bash completion output must not be empty")
	assert.Contains(t, out, "kardinal", "completion script must reference the binary name")
}

func TestCompletion_Zsh(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "zsh"})

	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, len(out) > 0, "zsh completion output must not be empty")
}

func TestCompletion_Fish(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "fish"})

	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, len(out) > 0, "fish completion output must not be empty")
}

func TestCompletion_PowerShell(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "powershell"})

	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, len(out) > 0, "powershell completion output must not be empty")
}

func TestCompletion_UnknownShell(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "tcsh"})

	err := root.Execute()
	assert.Error(t, err, "unknown shell must return an error")
}

func TestCompletion_NoArg(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion"})

	err := root.Execute()
	assert.Error(t, err, "missing shell argument must return an error")
}

func TestCompletion_HelpIncludesInstallInstructions(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "--help"})

	// --help exits with 0 via cobra
	_ = root.Execute()

	out := buf.String()
	assert.True(t, strings.Contains(out, "bash") || strings.Contains(out, "source"),
		"help text must mention bash or source")
}
