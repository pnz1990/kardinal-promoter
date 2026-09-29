## kardinal history

Show Bundle promotion history for a pipeline

### Synopsis

Show the promotion history for a Pipeline, including which Bundles
were promoted to which environments and when.

Output columns:
  BUNDLE      Bundle name
  ACTION      promote, or rollback when the Bundle is a rollback Bundle
  ENV         Target environment
  PR          Pull request number or --
  DURATION    Time from step creation to Verified (or to its last completed
              step when it failed); ... while running, -- when unknown
  TIMESTAMP   When the step was created

```
kardinal history <pipeline> [flags]
```

### Options

```
      --env string   Filter by environment name
  -h, --help         help for history
      --limit int    Maximum number of entries to show (default 20)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

