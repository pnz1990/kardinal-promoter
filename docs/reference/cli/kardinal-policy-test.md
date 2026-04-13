## kardinal policy test

Validate PolicyGate YAML syntax and dry-run CEL expressions

### Synopsis

Validate a PolicyGate YAML file: check CEL syntax and dry-run evaluate
each gate against a default context (current time, empty bundle).

No cluster access is required — all validation is performed locally.

Example:
  kardinal policy test policy-gates.yaml

```
kardinal policy test <file> [flags]
```

### Options

```
  -h, --help   help for test
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal policy](kardinal_policy.md)	 - Manage and evaluate promotion policy gates

