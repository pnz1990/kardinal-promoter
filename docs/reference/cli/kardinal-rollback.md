## kardinal rollback

Roll back a pipeline environment to a previous Bundle

### Synopsis

Roll back a pipeline environment to a previous Bundle.

Without --to, the target is the most recent Bundle, other than the one deployed
now, that was Verified in the environment and deploys different artifacts.
With --to, the named Bundle must belong to the pipeline, carry images or a
config commit, and differ from what is deployed now.

Creates a new Bundle that copies the target's images and config ref, sets
spec.provenance.rollbackOf to the target and intent.targetEnvironment to the
environment. It goes through the same PolicyGates and PR flow as any Bundle.

```
kardinal rollback <pipeline> [flags]
```

### Options

```
      --emergency    Emergency rollback: bypass skipPermission PolicyGates
      --env string   Target environment to roll back (required)
  -h, --help         help for rollback
      --to string    Specific Bundle name to roll back to
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

