## kardinal metrics

Show promotion metrics (DORA-style) for a pipeline

### Synopsis

Show deployment frequency, lead time, change failure rate and rollbacks for
<pipeline> over the last --days days, measured in <env> (default: the last
environment). With the defaults, the controller's own DORA metrics from the
Pipeline's status are shown when present.

The metrics are computed from the Bundles created in the last --days days and
their PromotionSteps in the environment:

    bundles_total          Bundles created in the window
    deployment_frequency   steps Verified in the environment per day
    lead_time_avg          mean time from Bundle creation to Verified
    change_fail_rate       Failed Bundles / bundles_total
    rollback_count         rollback Bundles in the window

When the environment is the pipeline's last one and --days is 30 (the
defaults, whether the flags are given or not), the controller's metrics from
Pipeline.status.deploymentMetrics are shown instead when present:
rollouts_last_30d, p50/p90_commit_to_prod, auto_rollback_rate,
operator_intervention_rate and stale_prod_days, over the last 30 Bundles
Verified in the last environment, and change_failure_rate and time_to_restore
(DORA stability) over the last 30 deployments to it.

```
kardinal metrics <pipeline> [<env>] [flags]
```

### Examples

```
  kardinal metrics my-app
  kardinal metrics my-app prod --days 7
```

### Options

```
      --days int          Lookback period in days (default 30)
  -e, --env string        Environment to measure (default: the pipeline's last environment)
  -h, --help              help for metrics
      --pipeline string   Pipeline name (or give it as the first argument)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

