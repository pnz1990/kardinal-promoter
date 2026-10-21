## kardinal completion

Generate shell completion scripts

### Synopsis

Print the completion script of kardinal for bash, zsh, fish or powershell.
Load it in the current shell, or install it permanently (examples below).
The bash script needs the bash-completion package.

Tab completes commands and flags, and the names they take, read from the
cluster of the current context (-n and --context apply): Pipelines, their
environments (the argument after the Pipeline, --env), Bundles with their
version and phase (reject, diff, delete bundle, rollback --to, logs
--bundle), version tags (approve --version) and gate names (override
--gate), in promotion order or newest first, those that start with what is
typed. A cluster that cannot be read in 3s (discovery included) completes no
names. rollback --to leaves out rejected versions.

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
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: budget, create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

