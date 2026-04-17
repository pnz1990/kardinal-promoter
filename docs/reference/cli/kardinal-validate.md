## kardinal validate

Validate Pipeline and PolicyGate YAML before applying to the cluster

### Synopsis

Validate a Pipeline or PolicyGate YAML file without connecting to the cluster.

Checks:
  - Schema: required fields present, valid enum values
  - Dependencies: no circular deps, all referenced environments exist  
  - CEL: PolicyGate expressions are syntactically valid (if present)
  - Lint: health.type set on environments with health configuration

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
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

