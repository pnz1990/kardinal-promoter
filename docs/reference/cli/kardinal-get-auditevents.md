## kardinal get auditevents

List AuditEvent records — immutable promotion event log

### Synopsis

List AuditEvents recording promotion lifecycle transitions.
AuditEvents are written by the controller at key points:
  PromotionStarted     — Bundle starts promoting through an environment
  PromotionSucceeded   — Health check passed; step reached Verified
  PromotionFailed      — Step reached Failed state
  PromotionSuperseded  — Newer Bundle superseded an in-flight promotion
  PromotionRejected    — kardinal reject cancelled an in-flight promotion
  GateEvaluated        — PolicyGate changed readiness state
  GateOverridden       — An override was recorded on a gate (verified author)
  RollbackStarted      — onHealthFailure=rollback triggered a rollback Bundle
  RollbackSucceeded    — A rollback Bundle's step reached Verified (written
                         besides PromotionSucceeded, once per step)

Events are listed most recent first, at most --limit of them. -o json and
-o yaml print the same events as a list of AuditEvent objects ([] when there
are none).

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
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

