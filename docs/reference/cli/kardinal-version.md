## kardinal version

Print the CLI, controller, and kro (Graph) versions

### Synopsis

Print the versions of the CLI, the controller and kro; the cluster ones are
unknown when the cluster cannot be reached.

Print the CLI version, the controller version (the kardinal-version ConfigMap
the controller writes to its namespace) and the kro version (the image tag of
the kro controller in kro-system). Cluster versions show as unknown when the
cluster cannot be reached.

```
kardinal version [flags]
```

### Examples

```
  kardinal version
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
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

