## kardinal override

Force-pass a PolicyGate with a mandatory audit record

### Synopsis

Make --gate pass for the in-progress versions of <pipeline> (in <env>, or every
environment), until --expires-in (default 1h). The override is recorded in the
gate's spec.overrides[] under your Kubernetes username, with a GateOverridden
AuditEvent; it applies to the gate instances that exist now, so run it while
the Bundle waits. A single gate instance name is accepted too.

```
kardinal override <pipeline> [<env>] --gate <gate> --reason <text> [flags]
```

### Examples

```
  kardinal override my-app prod --gate no-weekend-deploys --reason "P0 hotfix, incident 4521"

  # longer window; --stage is the older name of the environment flag
  kardinal override my-app --stage prod --gate change-freeze --reason "approved by CAB" --expires-in 4h
```

### Options

```
  -e, --env string            Environment the override applies to (or give it after the pipeline; default: every environment)
      --expires-in duration   How long the override is active (30m, 4h, 2d) (default 1h)
      --gate string           PolicyGate name to override
  -h, --help                  help for override
      --reason string         Mandatory justification for the override (audit record)
      --yes                   Do not ask for confirmation on a terminal
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

