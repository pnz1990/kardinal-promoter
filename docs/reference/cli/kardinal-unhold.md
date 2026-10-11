## kardinal unhold

Release an environment's hold (kardinal hold, or rollback --hold)

### Synopsis

Release the hold on <env>, whether kardinal hold or kardinal rollback --hold
put it there. The newest version held back then promotes into <env> through
its normal gates. release-hold is the same command.

```
kardinal unhold <pipeline> <env> [flags]
```

### Examples

```
  kardinal unhold my-app prod
  kardinal release-hold my-app prod
```

### Options

```
  -e, --env string   Held environment to release (or give it after the pipeline)
  -h, --help         help for unhold
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

