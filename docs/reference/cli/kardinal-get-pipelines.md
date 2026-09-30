## kardinal get pipelines

List Pipelines

### Synopsis

List Pipelines and their per-environment promotion status.

BUNDLE is the pipeline's current bundle: the newest bundle that is not
Superseded, whatever its phase (the newest one when all are Superseded), the
same bundle the UI shows. Every environment column describes that bundle: its
PromotionStep state there; Waiting when it has no PromotionStep there yet and
is still promoting (held by a PolicyGate, or an upstream environment is not
Verified yet). A dash means the bundle has no PromotionStep there and is
finished (Verified, Failed or Superseded) or skips the environment or stops
before it (skipEnvironments or targetEnvironment). kardinal status and kardinal
explain instead pick the current bundle per environment, so for an
environment the newest bundle has not reached they describe the bundle that
was there before.

The table needs list permission on bundles and promotionsteps; without it the
command fails instead of showing dashes.

Use --watch / -w to stream live updates (polls every 2s, Ctrl-C to quit).

When a Bundle promotion fails (e.g. due to an invalid dependsOn reference
or a circular dependency in the Pipeline spec), an ERROR: line is printed
after the table with the pipeline name and root cause:

  ERROR: pipeline my-app: build: environment "prod" dependsOn unknown environment "staging"

This avoids the need to run kubectl describe bundle to find the root cause
of a stalled promotion.

```
kardinal get pipelines [name] [flags]
```

### Options

```
  -A, --all-namespaces   List pipelines across all namespaces (adds NAMESPACE column)
  -h, --help             help for pipelines
  -w, --watch            Stream live updates (polls every 2s, Ctrl-C to quit)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources

