## kardinal metrics

Show promotion metrics (DORA-style) for a pipeline

### Synopsis

Show promotion performance metrics for a pipeline.

With --env and --days set, the metrics are computed from the Bundles created
in the last --days days and their PromotionSteps in --env:
  bundles_total          Bundles created in the window
  deployment_frequency   steps Verified in --env per day
  lead_time_avg          mean time from Bundle creation to Verified in --env
  change_fail_rate       Failed Bundles / bundles_total
  rollback_count         rollback Bundles in the window

With the defaults (--env is the pipeline's last environment, --days 30) the
controller's metrics from Pipeline.status.deploymentMetrics are shown instead
when present: rollouts_last_30d, p50/p90_commit_to_prod,
auto_rollback_rate, operator_intervention_rate and stale_prod_days, over
the last 30 Bundles Verified in the last environment.

Example:
  kardinal metrics --pipeline nginx-demo --env prod --days 30

```
kardinal metrics [flags]
```

### Options

```
      --days int          Lookback period in days (default 30)
      --env string        Target environment (default: the pipeline's last environment)
  -h, --help              help for metrics
      --pipeline string   Pipeline name (required)
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

