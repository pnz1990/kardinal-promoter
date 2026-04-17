## kardinal audit summary

Aggregate promotion metrics from AuditEvent records

### Synopsis

Show a summary of promotion activity from the AuditEvent log.

Includes: promotion counts, success rate, average duration, gate block rate, and rollbacks.

```
kardinal audit summary [flags]
```

### Options

```
  -h, --help              help for summary
      --pipeline string   Filter by pipeline name (default: all pipelines)
      --since string      Time window for events (e.g. 24h, 7d, 30d) (default "24h")
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal audit](kardinal_audit.md)	 - Audit log commands — view and summarize promotion events

