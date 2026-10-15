## kardinal pause

Pause a pipeline, or one fleet: no new promotion steps start

### Synopsis

Hold <pipeline>: no step starts, and a step still preparing its change holds
before its next step; a step waiting for its PR merge or health check finishes.
With --env <fleet>, pause one fleet: no further target starts, for any version,
and the targets in flight finish. Resume with kardinal resume (--env <fleet>).

Sets spec.paused on the Pipeline and creates the freeze PolicyGate
freeze-<pipeline>. While the pipeline is paused, no PromotionStep leaves
Pending and a step that is still preparing its change (clone, update,
commit, open PR) holds before its next step. A step that is waiting for a
PR merge or running its health check finishes, so a merged change is never
left unverified. Resume with: kardinal resume <pipeline>.

With --env <fleet> it adds the fleet environment to spec.pausedFleets
instead (update on pipelines/pause, as for spec.paused). The Graph of every
version in flight is rebuilt in place and starts no further target of the
fleet; steps that exist run to their end. kardinal resume <pipeline> --env
<fleet> removes the entry. A fleet target or an environment that is not a
fleet is refused.

```
kardinal pause <pipeline> [flags]
```

### Examples

```
  kardinal pause my-app --reason "INC-4211: hold releases while we investigate"
  kardinal resume my-app
  kardinal pause my-app --env prod      # prod is a fleet: stop starting targets
  kardinal resume my-app --env prod
```

### Options

```
      --env string      pause only this fleet environment: no further target starts
  -h, --help            help for pause
      --reason string   why, recorded with your name in the PipelinePaused AuditEvent
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

