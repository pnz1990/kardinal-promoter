## kardinal rollback

Roll back a pipeline environment to a previous Bundle

### Synopsis

Roll back <env> to the last Bundle Verified there that deploys something else
(or --to a named Bundle), as a new Bundle that goes through the gates, the PR
flow and every upstream environment first. With --hold and --reason, <env>
stays on the rollback until kardinal release-hold (or --hold-expires-in); its
blocking gates pass as audited exemptions meanwhile. See docs/rollback.md.

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
      --hold                       Keep the environment on the rollback until kardinal release-hold; the rollback passes blocking gates, each pass audited
      --hold-expires-in duration   End the hold this long after it is made (30m, 24h, 2d); default: until kardinal release-hold (default 0s)
      --reason string              Why the environment is held (required with --hold)
      --to string                  Specific Bundle name to roll back to
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

