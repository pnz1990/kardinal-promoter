## kardinal get auditevents

List AuditEvent records — immutable promotion event log

### Synopsis

List AuditEvents recording promotion lifecycle transitions.
AuditEvents are written by the controller at key points:
  PromotionStarted     — Bundle starts promoting through an environment
  PromotionSucceeded   — Health check passed; step reached Verified
  PromotionFailed      — Step reached Failed state
  PromotionSuperseded  — Newer Bundle superseded an in-flight promotion
  GateEvaluated        — PolicyGate changed readiness state
  RollbackStarted      — onHealthFailure=rollback triggered a rollback Bundle

```
kardinal get auditevents [flags]
```

### Options

```
      --bundle string     Filter by bundle name
      --env string        Filter by environment name
  -h, --help              help for auditevents
      --limit int         Maximum number of results to show (0 = unlimited) (default 20)
      --pipeline string   Filter by pipeline name
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal get](kardinal_get.md)	 - Display one or more kardinal resources

