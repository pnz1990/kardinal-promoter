## kardinal get subscriptions

List Subscriptions (passive artifact watchers)

### Synopsis

List Subscriptions and their watching status.

Subscriptions passively watch OCI registries or Git repositories and
automatically create Bundles when new artifacts are detected.

```
kardinal get subscriptions [name] [flags]
```

### Options

```
  -A, --all-namespaces   List subscriptions across all namespaces (adds NAMESPACE column)
  -h, --help             help for subscriptions
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

