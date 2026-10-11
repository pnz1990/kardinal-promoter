## kardinal release-hold

Release the hold of a rollback on an environment

### Synopsis

Release the hold kardinal rollback --hold put on <env>. The newest Bundle
held back then promotes into <env> through its normal gates.

```
kardinal release-hold <pipeline> <env> [flags]
```

### Examples

```
  kardinal release-hold my-app prod
  kardinal unhold my-app prod
```

### Options

```
  -e, --env string   Held environment to release (or give it after the pipeline)
  -h, --help         help for release-hold
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

