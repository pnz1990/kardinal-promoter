## kardinal override

Force-pass a PolicyGate with a mandatory audit record (K-09)

### Synopsis

Override a PolicyGate for a specific pipeline stage.

The override is time-limited and creates a mandatory audit record in
PolicyGate.spec.overrides[]. The gate passes immediately without evaluating
the CEL expression until the override expires.

All overrides are preserved for audit purposes. Use --expires-in to control
the override window (default: 1h).

Example:
  kardinal override my-app --stage prod --gate no-weekend-deploy \
    --reason "P0 hotfix — incident #4521"

```
kardinal override <pipeline> --stage <environment> --gate <gate-name> --reason <text> [--expires-in <duration>] [flags]
```

### Options

```
      --expires-in string   How long the override is active (Go duration, e.g. 1h, 4h, 30m) (default "1h")
      --gate string         PolicyGate name to override
  -h, --help                help for override
      --reason string       Mandatory justification for the override (audit record)
      --stage string        Environment (stage) name the override applies to
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

