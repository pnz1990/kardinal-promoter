## kardinal completion

Generate shell completion script

### Synopsis

Generate shell completion script for the specified shell.

Installation:

  Bash:
    # Add to ~/.bashrc or ~/.bash_profile:
    source <(kardinal completion bash)

  Zsh:
    # Add to ~/.zshrc (oh-my-zsh users should also add kardinal to the plugins list):
    source <(kardinal completion zsh)
    # Or install globally:
    kardinal completion zsh > "${fpath[1]}/_kardinal"

  Fish:
    kardinal completion fish | source
    # Or persist across sessions:
    kardinal completion fish > ~/.config/fish/completions/kardinal.fish

  PowerShell:
    kardinal completion powershell | Out-String | Invoke-Expression


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

