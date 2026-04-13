## kardinal explain

Explain the current state of a promotion pipeline

### Synopsis

Explain displays the PromotionSteps and PolicyGates for a pipeline.
It shows the current state, reason, and any PR URLs for each environment.

Use --env to filter to a specific environment.
Use --watch to stream live updates.

```
kardinal explain <pipeline> [flags]
```

### Options

```
      --env string   Filter to a specific environment
  -h, --help         help for explain
      --watch        Stream updates (polling)
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

