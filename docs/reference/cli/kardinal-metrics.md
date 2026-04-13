## kardinal metrics

Show promotion metrics (DORA-style) for a pipeline

### Synopsis

Show promotion performance metrics for a pipeline.

Metrics shown:
  DEPLOYMENT_FREQ   — bundles promoted to the target environment per day
  LEAD_TIME         — average time from bundle creation to prod verification
  FAIL_RATE         — percentage of bundles that reached Failed state
  ROLLBACK_COUNT    — number of rollback bundles in the period

Example:
  kardinal metrics --pipeline nginx-demo --env prod --days 30

```
kardinal metrics [flags]
```

### Options

```
      --days int          Lookback period in days (default 30)
      --env string        Target environment for lead time calculation (default "prod")
  -h, --help              help for metrics
      --pipeline string   Pipeline name (required)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "/Users/rrroizma/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

