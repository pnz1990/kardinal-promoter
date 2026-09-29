## kardinal get

Display one or more kardinal resources

```
kardinal get [flags]
```

### Options

```
  -h, --help   help for get
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes
* [kardinal get auditevents](kardinal-get-auditevents.md)	 - List AuditEvent records — immutable promotion event log
* [kardinal get bundles](kardinal-get-bundles.md)	 - List Bundles, optionally filtered by pipeline name
* [kardinal get pipelines](kardinal-get-pipelines.md)	 - List Pipelines
* [kardinal get steps](kardinal-get-steps.md)	 - List PromotionSteps for a pipeline
* [kardinal get subscriptions](kardinal-get-subscriptions.md)	 - List Subscriptions (passive artifact watchers)

