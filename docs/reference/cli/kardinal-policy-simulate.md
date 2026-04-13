## kardinal policy simulate

Simulate PolicyGate evaluation for a hypothetical promotion context

### Synopsis

Simulate PolicyGate evaluation.

Builds a mock CEL context from the provided flags and evaluates each
PolicyGate for the pipeline/environment against that context.

Example:
  kardinal policy simulate --pipeline nginx-demo --env prod --time "Saturday 3pm"
  # RESULT: BLOCKED
  # Blocked by: no-weekend-deploys

```
kardinal policy simulate [flags]
```

### Options

```
      --env string         Environment name (required)
  -h, --help               help for simulate
      --pipeline string    Pipeline name (required)
      --soak-minutes int   Simulated upstream soak time in minutes
      --time string        Simulated time (e.g. "Saturday 3pm", "Tuesday 10am")
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal policy](kardinal_policy.md)	 - Manage and evaluate promotion policy gates

