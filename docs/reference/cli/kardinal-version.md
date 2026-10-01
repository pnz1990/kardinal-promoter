## kardinal version

Print the CLI, controller, and kro (Graph) versions

### Synopsis

Print the CLI version, the controller version (the kardinal-version ConfigMap
the controller writes to its namespace) and the kro version (the image tag of
the kro controller in kro-system). Cluster versions show as unknown when the
cluster cannot be reached.

```
kardinal version [flags]
```

### Options

```
      --controller-namespace string   Namespace kardinal-promoter is installed in (default "kardinal-system")
  -h, --help                          help for version
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

