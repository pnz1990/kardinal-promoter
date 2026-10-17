## kardinal audit

Audit log commands: view and summarize promotion events

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
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes
* [kardinal audit evidence-pack](kardinal-audit-evidence-pack.md)	 - Write the records of a period, with a manifest of hashes and the control mapping, as one tar.gz
* [kardinal audit export](kardinal-audit-export.md)	 - Write AuditEvents as JSON lines or OCSF API Activity events, for a SIEM
* [kardinal audit summary](kardinal-audit-summary.md)	 - Aggregate promotion metrics from AuditEvent records

