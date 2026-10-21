## kardinal rollback

Roll back an environment to the version before

### Synopsis

Roll back <env> to the last version Verified there that deploys something
else (or --to a named Bundle), as a new Bundle through <env>'s gates and PR
flow; upstream environments that verified it are not promoted again. With
--hold and --reason, <env> stays on the rollback until kardinal unhold (or
--hold-expires-in); its blocking gates pass as audited exemptions. See docs/rollback.md.

```
kardinal rollback <pipeline> <env> [flags]
```

### Examples

```
  # roll prod back to the previous verified version
  kardinal rollback my-app prod

  # roll back to a named Bundle and keep prod there for a day
  kardinal rollback my-app prod --to my-app-v1-4 --hold --reason "incident 4521" --hold-expires-in 1d
```

### Options

```
  -e, --env string                 Environment to roll back (or give it after the pipeline)
  -h, --help                       help for rollback
      --hold                       Keep the environment on the rollback until kardinal unhold; the rollback passes blocking gates, each pass audited
      --hold-expires-in duration   End the hold this long after it is made (30m, 24h, 2d); default: until kardinal unhold
      --reason string              Why the environment is held (required with --hold)
      --to string                  The Bundle, or version (image tag), to roll back to (default: the version Verified there before)
      --yes                        Do not ask for confirmation on a terminal
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: budget, create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

