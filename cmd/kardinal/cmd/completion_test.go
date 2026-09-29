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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coreSubcommands are the CLI subcommands that must be reachable via completion.
// We verify these by exercising the __complete protocol directly, since cobra
// generates dynamic completion scripts that do not embed command names statically.
// approve is deprecated (it had no effect), so cobra leaves it out of completion.
var coreSubcommands = []string{"get", "explain", "logs", "status", "rollback", "override"}

// C09a-cli-22: each script starts with its shell's header and defines the
// kardinal entry point.
func TestCompletion_Scripts(t *testing.T) {
	cases := []struct {
		shell, header, entry string
	}{
		{"bash", "# bash completion V2 for kardinal", "__start_kardinal()"},
		{"zsh", "#compdef kardinal", "_kardinal()"},
		{"fish", "# fish completion for kardinal", "function __kardinal_perform_completion"},
		{"powershell", "# powershell completion for kardinal", "Register-ArgumentCompleter"},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			out, err := executeRoot(t, "completion", tc.shell)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(out, tc.header), "want header %q, got:\n%.80s", tc.header, out)
			assert.Contains(t, out, tc.entry)
		})
	}
}

// TestCompletion_CoreSubcommandsComplete verifies that core subcommands are
// reachable through cobra's __complete protocol. This catches command tree
// mis-wiring that would silently break power-user tab completion
// (design doc 15 §Future — kardinal completion CI test).
func TestCompletion_CoreSubcommandsComplete(t *testing.T) {
	// __complete with an empty word lists one "name\tdescription" per line,
	// then a ":<directive>" line.
	out, err := executeRoot(t, "__complete", "")
	require.NoError(t, err)
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		names[strings.SplitN(line, "\t", 2)[0]] = true
	}
	for _, sub := range coreSubcommands {
		assert.True(t, names[sub],
			"__complete output must list subcommand %q — check that newXxxCmd() is added to root via AddCommand", sub)
	}
}

func TestCompletion_ArgErrors(t *testing.T) {
	_, err := executeRoot(t, "completion", "tcsh")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid argument "tcsh" for "kardinal completion"`)

	_, err = executeRoot(t, "completion")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s), received 0")
}

func TestCompletion_HelpIncludesInstallInstructions(t *testing.T) {
	out, err := executeRoot(t, "completion", "--help")
	require.NoError(t, err)
	for _, want := range []string{
		"source <(kardinal completion bash)",
		"kardinal completion zsh",
		"kardinal completion fish > ~/.config/fish/completions/kardinal.fish",
		"kardinal completion powershell >> $PROFILE",
	} {
		assert.Contains(t, out, want)
	}
}
