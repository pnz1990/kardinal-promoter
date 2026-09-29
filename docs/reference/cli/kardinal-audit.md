## kardinal audit

Audit log commands — view and summarize promotion events

```
kardinal audit [flags]
```

### Options

```
  -h, --help   help for audit
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
* [kardinal audit summary](kardinal-audit-summary.md)	 - Aggregate promotion metrics from AuditEvent records

