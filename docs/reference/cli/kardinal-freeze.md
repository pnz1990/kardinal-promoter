## kardinal freeze

Freeze agents, or stop the line for everyone; with no flags, list the freezes

### Synopsis

Hold promotions until the freeze is lifted: --agents holds the versions agents
created, --everyone every version (stop-the-line), in every namespace or with -n
one. --for ends it by itself; otherwise kardinal unfreeze does. Let a pipeline
through with kardinal override <pipeline> <env> --freeze. With no flags it lists
the freezes.

A freeze is a cluster-scoped ChangeWindow of type blackout with spec.freeze,
named kardinal-freeze-<agents|everyone>[-<namespace>]. While it is active,
the controller holds every PromotionStep it selects, in every Graph,
including those built before the freeze: the step does not start, or does
not run its next git step, and its message names the freeze, who froze and
why. Steps waiting for a PR merge or a health check finish. A repeated
freeze of the same scope updates its reason and end. Freezing needs create
and update on changewindows (cluster-scoped); the chart's freezes admission
policy records you as spec.freeze.createdBy. kardinal override <pipeline>
<env> --freeze adds a time-limited exemption to the active freezes, recorded
with your name and audited (FreezeExempted) when a step goes on under it.

```
kardinal freeze [--agents | --everyone] [--cluster | -n <ns>] --reason <text> [--for <duration>] [flags]
```

### Examples

```
  kardinal freeze --agents --reason "INC-4211: agents paused while we investigate"
  kardinal freeze --everyone -n payments --reason "payments incident" --for 2h
  kardinal freeze
  kardinal unfreeze kardinal-freeze-agents
```

### Options

```
      --agents          Hold the versions agents created (an agent freeze)
      --cluster         Every namespace (the default; -n limits the freeze to one namespace)
      --everyone        Hold every version (stop-the-line)
      --for duration    End the freeze after this long (30m, 4h, 2d); default: until kardinal unfreeze
  -h, --help            help for freeze
      --reason string   Why (shown on every held step)
      --yes             Do not ask for confirmation on a terminal
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

