## kardinal get bundles

List Bundles, optionally filtered by pipeline name

### Synopsis

List Bundles, newest first: by creation time, then, within the same
second, by the kardinal.io/created-at annotation (sub-second), then by name;
the order supersession uses and kardinal history lists. -o json and -o yaml
list them in the same order.

```
kardinal get bundles [pipeline] [flags]
```

### Examples

```
  kardinal get bundles my-app --active
  kardinal get bundles -A -o json
```

### Options

```
      --active           Hide Superseded and Rejected bundles
  -A, --all-namespaces   List bundles across all namespaces (adds NAMESPACE column)
  -h, --help             help for bundles
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

