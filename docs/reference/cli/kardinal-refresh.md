## kardinal refresh

Force re-reconciliation of a Pipeline

### Synopsis

Make the controller re-read <pipeline> now (status, Ready condition, metrics,
pause gate). It does not retry a Bundle, re-evaluate gates or re-run health
checks: those re-check on their own intervals.

```
kardinal refresh <pipeline> [flags]
```

### Examples

```
  kardinal refresh my-app
```

### Options

```
  -h, --help   help for refresh
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

