## kardinal rollback

Roll back a pipeline environment to a previous Bundle

### Synopsis

Roll back a pipeline environment to a previous Bundle.

Creates a new Bundle with spec.provenance.rollbackOf pointing to the
target Bundle. Goes through the same pipeline, PolicyGates, and PR flow.

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

