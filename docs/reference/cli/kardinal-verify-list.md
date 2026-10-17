## kardinal verify list

List the verdicts on a version, and how its verify gates count them

```
kardinal verify list <pipeline|bundle> [flags]
```

### Examples

```
  # the verdicts on the version that runs in uat
  kardinal verify list checkout --env uat

  kardinal verify list checkout-1-5-0 --check exploratory-qa
```

### Options

```
      --check string   Only verdicts for this check
  -e, --env string     Only verdicts for this environment
  -h, --help           help for list
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal verify](kardinal-verify.md)	 - Record a verdict on the version running in an environment (verify gates)

