## kardinal delete bundle

Delete a Bundle by name

### Synopsis

Delete a Bundle by name.

Deleting a Bundle stops its promotion: its Graph, PromotionSteps and gate
instances are deleted with it. Nothing is written to git. A pull request the
promotion opened that is still open is closed, with a comment, before its
PromotionStep goes away.

Finished Bundles (Verified, Failed or Superseded) beyond the Pipeline's
spec.historyLimit (default 50) are deleted automatically, oldest first;
use this command to remove a Bundle before that.

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
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal delete](kardinal-delete.md)	 - Delete kardinal resources

