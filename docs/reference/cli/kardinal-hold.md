## kardinal hold

Keep an environment on what runs there now

### Synopsis

Hold <env> on the version that runs there now: no other version promotes into
it until kardinal unhold (or --expires-in). Nothing is rolled back and no
Bundle is created; to go back to an earlier version and keep it, use
kardinal rollback <pipeline> <env> --hold.

```
kardinal hold <pipeline> <env> --reason <text> [flags]
```

### Examples

```
  kardinal hold my-app prod --reason "freeze during the audit"
  kardinal hold my-app prod --reason "INC-4521" --expires-in 4h
  kardinal unhold my-app prod
```

### Options

```
  -e, --env string            Environment to hold (or give it after the pipeline)
      --expires-in duration   End the hold this long after it is made (30m, 4h, 2d); default: until kardinal unhold
  -h, --help                  help for hold
      --reason string         Why the environment is held (required)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

