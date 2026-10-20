## kardinal audit export

Write AuditEvents as JSON lines or OCSF API Activity events, for a SIEM

### Synopsis

Write the AuditEvents of the last --since as newline-delimited JSON, one
event per line: --format json (the AuditEvent spec, with its namespace, name
and uid) or --format ocsf (OCSF 1.3.0 API Activity, class 6003). Each event
names its actor: who caused it, verified, and what the client asserted about
itself, kept apart (docs/actors.md).

Events are written in the order the API server lists them, page by page
(500 a page), not sorted: sort by time downstream (spec.timestamp, OCSF time).
Run it on a schedule more often than AuditEvent retention, and deduplicate by
uid. The OCSF mapping: actor.user.name is the principal, actor.user.type
User (human), System (bot, controller) or Agent, actor.app_name the agent;
api.operation is the AuditEvent action; resources are the Bundle, Pipeline
and environment. The actor's sponsor and asserted fields are in
unmapped.kardinal.actor.

```
kardinal audit export [flags]
```

### Examples

```
  kardinal audit export --since 7d > audit.ndjson
  kardinal audit export -A --format ocsf --file audit.ocsf.ndjson
  kardinal audit export --pipeline checkout --since 1h
```

### Options

```
  -A, --all-namespaces    Events of every namespace
      --file string       Write to this file instead of standard output
      --format string     json (the AuditEvent spec) or ocsf (OCSF 1.3.0 API Activity) (default "json")
  -h, --help              help for export
      --pipeline string   Only this pipeline's events
      --since string      Time window: a positive duration (90m, 24h, 7d) (default "24h")
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal audit](kardinal-audit.md)	 - Audit log commands: view and summarize promotion events

