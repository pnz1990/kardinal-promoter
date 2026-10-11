## kardinal get auditevents

List AuditEvent records: the immutable promotion event log

### Synopsis

List the AuditEvents the controller writes at each promotion, gate, approval
and rollback transition, most recent first (at most --limit).

List AuditEvents recording promotion lifecycle transitions.
AuditEvents are written by the controller at key points:
  PromotionStarted     — Bundle starts promoting through an environment
  PromotionSucceeded   — Health check passed; step reached Verified
  PromotionFailed      — Step reached Failed state
  PromotionSuperseded  — Newer Bundle superseded an in-flight promotion
  PromotionRejected    — kardinal reject cancelled an in-flight promotion
  GateEvaluated        — PolicyGate changed readiness state
  GateOverridden       — An override was recorded on a gate (verified author)
  ApprovalRecorded     — An approval gate saw a kardinal approve decision
  ApprovalRevoked      — An approval gate saw that decision's Approval deleted
  RollbackStarted      — onHealthFailure=rollback triggered a rollback Bundle
  RollbackSucceeded    — A rollback Bundle's step reached Verified (written
                         besides PromotionSucceeded, once per step)

Events are listed most recent first, at most --limit of them. -o json and
-o yaml print the same events as a list of AuditEvent objects ([] when there
are none).

```
kardinal get auditevents [flags]
```

### Examples

```
  kardinal get auditevents --pipeline my-app --env prod
  kardinal get ae -A --limit 50
```

### Options

```
  -A, --all-namespaces    List events across all namespaces (adds NAMESPACE column)
      --bundle string     Filter by Bundle, by name or version
  -e, --env string        Filter by environment name
  -h, --help              help for auditevents
      --limit int         Maximum number of results to show (0 = unlimited) (default 20)
      --pipeline string   Filter by pipeline name
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

