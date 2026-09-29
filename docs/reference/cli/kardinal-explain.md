## kardinal explain

Explain the current state of a promotion pipeline

### Synopsis

Explain displays, per environment, the PromotionStep and the PolicyGates of
the Bundle currently promoting there (or last promoted). Gates include org
gates from the policy namespaces: they are the instances the Graph created for
that Bundle, with the controller's latest evaluation.

Use --env to filter to a specific environment.
Use --watch to refresh every 3 seconds.
Use --color to force ANSI color output (auto-detected when writing to a TTY).

```
kardinal explain <pipeline> [flags]
```

### Options

```
      --color        Force ANSI color output (auto-detected when TTY)
      --env string   Filter to a specific environment
  -h, --help         help for explain
      --watch        Refresh every 3 seconds
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

