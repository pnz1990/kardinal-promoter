## kardinal refresh

Force re-reconciliation of a Pipeline (Kargo parity)

### Synopsis

Force the controller to re-reconcile a Pipeline immediately.

Adds a kardinal.io/refresh annotation to the Pipeline, which triggers the
controller to run a reconciliation cycle. Useful when you need to re-evaluate
PolicyGates, re-check health adapters, or force a retry after a transient error.

Example:
  kardinal refresh nginx-demo

```
kardinal refresh <pipeline> [flags]
```

### Options

```
  -h, --help   help for refresh
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "/Users/rrroizma/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

