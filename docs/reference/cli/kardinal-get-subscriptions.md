## kardinal get subscriptions

List Subscriptions (passive artifact watchers)

### Synopsis

List Subscriptions: watchers of an OCI registry or a git repository that create
a Bundle when a new artifact appears.

Subscriptions passively watch OCI registries or Git repositories and
automatically create Bundles when new artifacts are detected.

```
kardinal get subscriptions [name] [flags]
```

### Examples

```
  kardinal get subscriptions
  kardinal get subscriptions -A
```

### Options

```
  -A, --all-namespaces   List subscriptions across all namespaces (adds NAMESPACE column)
  -h, --help             help for subscriptions
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: status, why)
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

