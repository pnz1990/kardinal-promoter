## kardinal get bundles

List Bundles, optionally filtered by pipeline name

### Synopsis

List Bundles, newest first: by the kardinal.io/created-at annotation, then
creation time and name, the order supersession uses and kardinal history lists.

```
kardinal get bundles [pipeline] [flags]
```

### Options

```
      --active   Hide Superseded bundles
  -h, --help     help for bundles
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

