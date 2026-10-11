## kardinal policy

Manage and evaluate promotion policy gates

```
kardinal policy [flags]
```

### Options

```
  -h, --help   help for policy
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes
* [kardinal policy list](kardinal-policy-list.md)	 - List PolicyGates
* [kardinal policy simulate](kardinal-policy-simulate.md)	 - Simulate PolicyGate evaluation for a hypothetical promotion context
* [kardinal policy test](kardinal-policy-test.md)	 - Validate PolicyGate YAML syntax and dry-run CEL expressions

