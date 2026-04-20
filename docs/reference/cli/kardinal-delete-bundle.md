## kardinal delete bundle

Delete a Bundle by name

### Synopsis

Delete a Bundle by name.

Deleting a Bundle cancels any in-progress promotion for that Bundle.
Superseded Bundles are deleted automatically by the garbage collector;
use this command to explicitly remove a Bundle before that occurs.

```
kardinal delete bundle <name> [flags]
```

### Options

```
  -h, --help   help for bundle
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal delete](kardinal_delete.md)	 - Delete kardinal resources

