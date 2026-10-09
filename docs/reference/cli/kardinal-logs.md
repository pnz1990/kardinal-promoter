## kardinal logs

Show promotion step execution logs for a pipeline

### Synopsis

Show the execution history and output of PromotionSteps for a pipeline.

It shows the PromotionSteps of every Bundle of the pipeline that is not
Superseded (all of them if those have none), or of the Bundle --bundle names.
For each PromotionStep, it shows:
  - Current state (Promoting, WaitingForMerge, HealthChecking, Verifying, Verified, Failed)
  - Step message (error details, health check results, PR URLs)
  - Step outputs (branch name, PR URL, PR number)
  - Conditions from the status
  - The steps it ran (status.steps), with state and duration

Use --follow (-f) to stream step progress in real time, polling every 2 seconds.
It exits when every step shown is in a terminal state (Verified, Failed,
AbortedByAlarm or RollingBack) and no Bundle it follows is still promoting.
With --env it exits once that environment's steps are terminal.
Each state change is printed once.

Example:
  kardinal logs nginx-demo
  kardinal logs nginx-demo --env prod
  kardinal logs nginx-demo --bundle nginx-demo-v1-29-0
  kardinal logs nginx-demo --follow

```
kardinal logs <pipeline> [flags]
```

### Options

```
      --bundle string   Show logs for one bundle (default: every bundle that is not Superseded)
      --env string      Filter by environment
  -f, --follow          Stream step progress, polling every 2s until the promotion ends
  -h, --help            help for logs
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

