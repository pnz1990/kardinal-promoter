## kardinal promote

Promote the Bundle verified upstream into an environment

### Synopsis

Promote the newest Bundle that is Verified in every upstream environment
into <env>, as a new Bundle that targets it; gates and approval apply.
It refuses, creating nothing, when nothing is Verified upstream yet, when
that Bundle is already in <env>, or when a newer Bundle is still promoting.
The first environment has nothing upstream: use kardinal ship there.

```
kardinal promote <pipeline> <env> [flags]
```

### Examples

```
  # promote what uat verified into prod
  kardinal promote my-app prod

  # the same, with the flag form
  kardinal promote my-app --env prod
```

### Options

```
  -e, --env string   Target environment (or give it after the pipeline)
  -h, --help         help for promote
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

