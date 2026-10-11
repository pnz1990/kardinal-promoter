## kardinal resume

Resume a paused pipeline

### Synopsis

Resume <pipeline> after kardinal pause: its held steps continue and new
promotions start again.

```
kardinal resume <pipeline> [flags]
```

### Examples

```
  kardinal resume my-app
```

### Options

```
  -h, --help   help for resume
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

