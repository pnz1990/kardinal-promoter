## kardinal validate

Validate Pipeline and PolicyGate YAML before applying to the cluster

### Synopsis

Validate Pipeline and PolicyGate YAML without connecting to the cluster.
The file may hold several documents; each Pipeline and PolicyGate is checked.

Checks:
  - Pipeline: at least one environment, every environment named, spec.git.url
    set, and the environment dependencies form a valid graph (no cycles, no
    unknown dependsOn)
  - PolicyGate: spec.expression set and compiles with the controller's
    PolicyGate CEL environment

This is not full CRD schema validation; 'kubectl apply --dry-run=server'
checks the schema.

Exit codes:
  0 — file is valid
  1 — validation failed (actionable errors printed)

```
kardinal validate [flags]
```

### Options

```
  -f, --file string   Path to Pipeline or PolicyGate YAML file (required)
  -h, --help          help for validate
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

