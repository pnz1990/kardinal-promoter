## kardinal get bundles

List Bundles, optionally filtered by pipeline name

```
kardinal get bundles [pipeline] [flags]
```

### Options

```
      --active   Show only active bundles (Promoting/Verified/Failed — excludes Superseded)
  -h, --help     help for bundles
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

