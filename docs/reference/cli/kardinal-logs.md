## kardinal logs

Show promotion step execution logs for a pipeline (Kargo parity)

### Synopsis

Show the execution history and output of PromotionSteps for a pipeline.

For each active PromotionStep, shows:
  - Current state (Promoting, WaitingForMerge, HealthChecking, Verified, Failed)
  - Step message (error details, health check results, PR URLs)
  - Step outputs (branch name, PR URL, PR number)
  - Conditions from the status

Example:
  kardinal logs nginx-demo
  kardinal logs nginx-demo --env prod
  kardinal logs nginx-demo --bundle nginx-demo-v1-29-0

```
kardinal logs <pipeline> [flags]
```

### Options

```
      --bundle string   Show logs for a specific bundle (default: most recent active)
      --env string      Filter by environment
  -h, --help            help for logs
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

