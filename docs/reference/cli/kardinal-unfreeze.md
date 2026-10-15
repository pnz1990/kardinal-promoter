## kardinal unfreeze

Lift a freeze

### Synopsis

Delete a freeze: by name (kardinal freeze lists them), or by the scope it was
created with. Held steps go on within a minute. A ChangeWindow that is not a
freeze is never deleted.

```
kardinal unfreeze [<name> | --agents | --everyone] [--cluster | -n <ns>] [flags]
```

### Examples

```
  kardinal unfreeze kardinal-freeze-agents
  kardinal unfreeze --everyone -n payments
```

### Options

```
      --agents     Hold the versions agents created (an agent freeze)
      --cluster    Every namespace (the default; -n limits the freeze to one namespace)
      --everyone   Hold every version (stop-the-line)
  -h, --help       help for unfreeze
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

