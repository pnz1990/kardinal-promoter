## kardinal release-hold

Release the hold of a rollback on an environment

### Synopsis

Release the hold kardinal rollback --hold put on an environment.

Removes the environment from the Pipeline's spec.holds. The newest Bundle that
was held back then promotes into the environment through its normal gates,
and the rollback Bundle's gates are evaluated without the exemption again.

```
kardinal release-hold <pipeline> [flags]
```

### Options

```
      --env string   Held environment to release (required)
  -h, --help         help for release-hold
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

