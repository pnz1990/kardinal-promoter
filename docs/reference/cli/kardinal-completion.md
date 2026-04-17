## kardinal completion

Generate shell completion scripts

### Synopsis

Generate shell completion scripts for kardinal.

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


```
kardinal completion [bash|zsh|fish|powershell]
```

### Options

```
  -h, --help   help for completion
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

