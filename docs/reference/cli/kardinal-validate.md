## kardinal validate

Validate Pipeline and PolicyGate YAML before applying to the cluster

### Synopsis

Validate Pipeline and PolicyGate YAML without connecting to the cluster.
The file may hold several documents; each kardinal.io Pipeline and PolicyGate
is checked. Other objects (a Namespace, another API group's Pipeline) are
skipped with a note.

Checks:
  - Pipeline: at least one environment, every environment named, spec.git.url
    set, the environment dependencies form a valid graph (no cycles, no
    unknown dependsOn), and no reserved field that is not implemented is set
    (steps, promotionTemplate, autoRollback, two or more regions,
    layout: branch, health.cluster, a health.resource.kind other than
    Deployment). The controller reports the same fields as
    Ready=False/NotImplemented on the Pipeline. With metadata.namespace set,
    a git.secretRef in another namespace is an error too (the controller
    reports it as Ready=False/ValidationFailed).
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

