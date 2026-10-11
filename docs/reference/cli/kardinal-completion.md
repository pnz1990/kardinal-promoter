## kardinal completion

Generate shell completion scripts

### Synopsis

Print the completion script of kardinal for bash, zsh, fish or powershell.
Load it in the current shell, or install it permanently (examples below).
The bash script needs the bash-completion package.

The bash script needs the bash-completion package (it calls
_get_comp_words_by_ref); install it with your OS package manager first.

To load completions immediately in the current shell session:

  # Bash
  source <(kardinal completion bash)

  # Zsh (compinit must be enabled)
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

```
kardinal completion [bash|zsh|fish|powershell]
```

### Examples

```
  # load in the current shell
  source <(kardinal completion bash)
  source <(kardinal completion zsh)
  kardinal completion fish | source
  kardinal completion powershell | Out-String | Invoke-Expression

  # install
  kardinal completion bash > /etc/bash_completion.d/kardinal
  kardinal completion zsh > "${fpath[1]}/_kardinal"
  kardinal completion fish > ~/.config/fish/completions/kardinal.fish
  kardinal completion powershell >> $PROFILE
```

### Options

```
  -h, --help   help for completion
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

