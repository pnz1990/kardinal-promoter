## kardinal status

Show controller health or per-pipeline in-flight promotion details

### Synopsis

Show the health of the kardinal controller and cluster resource summary.

When called without arguments: displays the controller version (the
kardinal-version ConfigMap in --controller-namespace), the pipeline count with
any Degraded pipelines, and the bundle count (active = Available or Promoting).

When called with a pipeline name: shows in-flight promotion details for that
pipeline — the current bundle per environment (the newest bundle that is not
Superseded and has a PromotionStep there, or a gate instance there and has not
failed; see kardinal explain), its PromotionSteps
(one row per region, active steps marked, with the Bundle each row belongs
to), the Bundle deployed in every environment (the one whose change landed
there last, as kardinal rollback judges it, with its image tags or config
commit; "none" when no change has landed yet), the PolicyGates holding it back
(with CEL expression and current reason), and open PR URLs. A gate is listed
as blocking only while it holds the bundle back: it is not ready and either
every upstream environment is Verified for that bundle and the bundle has no
PromotionStep in the gate's environment yet, or it is a pre-deploy gate that
the bundle's Pending PromotionStep there waits on. A gate of an environment
the bundle has not reached yet is not listed. This is the first command to
run when a promotion is stuck.

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
      --controller-namespace string   Namespace kardinal-promoter is installed in (default "kardinal-system")
  -h, --help                          help for status
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

