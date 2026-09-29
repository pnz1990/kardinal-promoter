## kardinal policy list

List PolicyGates

### Synopsis

List PolicyGate templates.

Without --pipeline, lists every template in every namespace. With --pipeline,
lists the templates the controller attaches to that pipeline's environments.

The CEL column is the controller's syntax check of the expression: valid,
invalid (see kubectl describe), or - when not checked yet.

```
kardinal policy list [flags]
```

### Options

```
  -h, --help                        help for list
      --pipeline string             Show only the gates attached to this pipeline
      --policy-namespaces strings   Namespaces the controller reads org PolicyGates from (its --policy-namespaces flag); a Pipeline's spec.policyNamespaces and its own namespace are read as well (default [platform-policies])
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates

