## kardinal history

Show a pipeline's promotion history, newest first

### Synopsis

Show which versions were promoted to (or rolled back in) each environment of
<pipeline>, newest first: the version, promote or rollback, the environment,
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
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

