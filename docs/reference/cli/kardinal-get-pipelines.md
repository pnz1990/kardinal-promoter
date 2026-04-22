## kardinal get pipelines

List Pipelines

### Synopsis

List Pipelines and their per-environment promotion status.

Use --watch / -w to stream live updates (polls every 2s, Ctrl-C to quit).

When a Bundle promotion fails (e.g. due to an invalid dependsOn reference
or a circular dependency in the Pipeline spec), an ERROR: line is printed
after the table with the pipeline name and root cause:

  ERROR: pipeline my-app: build: environment "prod" dependsOn unknown environment "staging"

This avoids the need to run kubectl describe bundle to find the root cause
of a stalled promotion.

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

* [kardinal get](kardinal_get.md)	 - Display one or more kardinal resources

