## kardinal logs

Show promotion step execution logs for a pipeline

### Synopsis

Show each PromotionStep of <pipeline>'s Bundles that are not Superseded (or of
--bundle): its state, message, outputs (branch, PR), conditions, and the steps
it ran with their durations. -f follows: it polls every 2s, prints each change
once, and exits when every step shown (only <env>'s, if given) has finished.

```
kardinal logs <pipeline> [<env>] [flags]
```

### Examples

```
  kardinal logs my-app
  kardinal logs my-app prod -f
  kardinal logs my-app --bundle my-app-v1-29-0
```

### Options

```
      --bundle string   Show logs for one bundle (default: every bundle that is not Superseded)
  -e, --env string      Only this environment (or give it after the pipeline)
  -f, --follow          Stream step progress, polling every 2s until the promotion ends
  -h, --help            help for logs
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

