## kardinal explain

Explain the current state of a promotion pipeline

### Synopsis

Explain displays, per environment, the PromotionStep and the PolicyGates of
the current Bundle there: the newest Bundle that is not Superseded and has a
PromotionStep in that environment, or a gate instance there and has not
failed. The Graph creates a Bundle's gate instances when the Bundle starts, so
an environment a promoting Bundle has not reached yet shows the gates it will
wait on; one a Failed Bundle never reached keeps the Bundle before it. When
every Bundle there is Superseded, the newest one with a PromotionStep there is
shown. Gates include org
gates from the policy namespaces and skip-permission gates: they are the
instances the Graph created for that Bundle, with the controller's latest
evaluation. Gates that are not ready are listed first. BUNDLE names the
Bundle each row belongs to.

An environment where that Bundle's change has not landed yet (it has not
reached the environment, or its PR is not merged) also gets a line after the
table with the Bundle deployed there now: the one whose change landed last,
as kardinal rollback judges it, with its image tags or config commit.
Image and config Bundles do not supersede each other: when the last one is an
image Bundle and a config commit landed before it, the line adds
"; config <sha> from <bundle>", and under a config Bundle it adds
"; images <tags> from <bundle>". "deployed: none" means no change has landed
there yet.

    prod   deployed: app-v1 (sha-1a2b3c4)
    uat    deployed: app-v2 (sha-5d6e7f8); config 9f8e7d6 from app-cfg1

A gate's STATE is the one the UI shows, the first that applies:

    Pass        ready
    Block       holding the Bundle: every upstream environment is Verified
                and the environment has no step yet, or the gate holds
                the environment's Pending step
    Superseded  the Bundle was superseded; the gate is not evaluated again
    Rejected    the Bundle was rejected (kardinal reject); not evaluated again
    Pending     not evaluated yet
    Waiting     evaluated not ready, not holding the Bundle: the Bundle has
                not reached the environment, or it failed (it can retry)

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
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

