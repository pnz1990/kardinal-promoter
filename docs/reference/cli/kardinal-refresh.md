## kardinal refresh

Force re-reconciliation of a Pipeline

### Synopsis

Force the controller to re-reconcile a Pipeline immediately.

Sets the kardinal.io/refresh annotation on the Pipeline to the current time.
The change requeues the Pipeline reconciler, which re-derives the Pipeline's
status (phase, Ready condition, deployment metrics) and its pause freeze gate.
Bundle reconcilers follow Pipeline spec changes only, so this does not retry
a Bundle. PolicyGates and PromotionSteps do not watch Pipelines, so this
does not re-evaluate gates or re-run health checks; they re-check on their
own intervals.

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
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

