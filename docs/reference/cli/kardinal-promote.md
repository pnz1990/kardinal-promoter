## kardinal promote

Trigger promotion of a pipeline to a specific environment

### Synopsis

Trigger promotion by creating a Bundle that targets a specific environment.

The Bundle flows through all upstream environments first, then targets the
specified environment. PolicyGates and approval mode apply as configured.

```
kardinal promote <pipeline> --env <environment> [flags]
```

### Options

```
  -e, --env string   Target environment name (required)
  -h, --help         help for promote
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "/Users/rrroizma/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

