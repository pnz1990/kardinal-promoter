## kardinal resume

Resume a paused pipeline, or a paused fleet

### Synopsis

Resume <pipeline> after kardinal pause: its held steps continue and new
promotions start again. With --env <fleet>, resume a fleet paused with
kardinal pause --env <fleet>.

```
kardinal resume <pipeline> [flags]
```

### Examples

```
  kardinal resume my-app
  kardinal resume my-app --env prod
```

### Options

```
      --env string      resume only this fleet environment
  -h, --help            help for resume
      --reason string   why, recorded with your name in the PipelineResumed AuditEvent
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

