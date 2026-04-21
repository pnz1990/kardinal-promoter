## kardinal status

Show controller health or per-pipeline in-flight promotion details

### Synopsis

Show the health of the kardinal controller and cluster resource summary.

When called without arguments: displays controller version, pipeline count, and
active bundle count.

When called with a pipeline name: shows in-flight promotion details for that
pipeline — active bundle, PromotionStep states (with active steps highlighted),
blocking PolicyGates (with CEL expression and current reason), and open PR URLs.
This is the first command to run when a promotion is stuck.

Examples:
  # Cluster-level summary
  kardinal status

  # Per-pipeline in-flight view
  kardinal status nginx-demo

For detailed gate diagnostics, use 'kardinal explain <pipeline>'.
For step-level log output, use 'kardinal logs <pipeline>'.

```
kardinal status [pipeline] [flags]
```

### Options

```
  -h, --help   help for status
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

