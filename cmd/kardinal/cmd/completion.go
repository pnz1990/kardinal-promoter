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
	"fmt"

	"github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for kardinal.

To load completions immediately in the current shell session:

  # Bash
  source <(kardinal completion bash)

  # Zsh — must have compinit enabled
  source <(kardinal completion zsh)

  # Fish
  kardinal completion fish | source

  # PowerShell
  kardinal completion powershell | Out-String | Invoke-Expression

To install completions permanently:

  # Bash (Linux)
  kardinal completion bash > /etc/bash_completion.d/kardinal

  # Bash (macOS with Homebrew bash-completion@2)
  kardinal completion bash > $(brew --prefix)/etc/bash_completion.d/kardinal

  # Zsh
  kardinal completion zsh > "${fpath[1]}/_kardinal"

  # Fish
  kardinal completion fish > ~/.config/fish/completions/kardinal.fish

  # PowerShell
  kardinal completion powershell >> $PROFILE
`,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(out)
			default:
				return fmt.Errorf("unsupported shell %q — choose from: bash, zsh, fish, powershell", args[0])
			}
		},
	}
	return cmd
}
