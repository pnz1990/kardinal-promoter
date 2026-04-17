## kardinal get pipelines

List Pipelines

### Synopsis

List Pipelines and their per-environment promotion status.

Use --watch / -w to stream live updates (polls every 2s, Ctrl-C to quit).

```
kardinal get pipelines [name] [flags]
```

### Options

```
  -A, --all-namespaces   List pipelines across all namespaces (adds NAMESPACE column)
  -h, --help             help for pipelines
  -w, --watch            Stream live updates (polls every 2s, Ctrl-C to quit)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

