## kardinal history

Show Bundle promotion history for a pipeline

### Synopsis

Show which Bundles were promoted to (or rolled back in) each environment of
<pipeline>, newest first: the Bundle, promote or rollback, the environment,
the PR, how long it took, and when it started.

```
kardinal history <pipeline> [<env>] [flags]
```

### Examples

```
  kardinal history my-app
  kardinal history my-app prod --limit 5
```

### Options

```
  -e, --env string   Only this environment (or give it after the pipeline)
  -h, --help         help for history
      --limit int    Maximum number of entries to show (default 20)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

