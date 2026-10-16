# Pause and Resume

During an incident, you may want to stop promotions without rolling back.

```bash
# Pause: no new promotion step starts; in-flight steps hold at the next safe point
kardinal pause my-app --reason "INC-4211: bad config in prod"

# Resume: held steps continue where they stopped
kardinal resume my-app
```

`kardinal pause` (and the UI Pause button) sets `spec.paused: true` on the Pipeline, and the controller keeps a freeze gate named `freeze-<pipeline>` in the Pipeline's namespace while it is set. While the Pipeline is paused:
- No PromotionStep leaves `Pending`, so no new environment starts promoting.
- A step in `Promoting` (clone, update manifests, commit, open PR) holds before its next git step. Its status message says the pipeline is paused.
- A step in `WaitingForMerge` or `HealthChecking` finishes. Stopping it would leave a merged change unverified. Open PRs stay open; merging one during a pause still deploys it.
- A [pre hook](hooks.md) that has not started its Job waits in `Pending`, and its timeout does not count. Post hooks keep running, like `HealthChecking` steps: their change is already deployed. A hook whose Job is running finishes.
- New versions are still accepted, but their steps wait in `Pending`.

After resume, held steps continue from where they stopped. A held step re-checks the pause every minute, so this takes up to a minute. A waiting pre hook starts as soon as the freeze gate is deleted. No re-trigger is needed.

The pause is checked before each step and each pre hook starts its next piece of work, not continuously. A pause that lands while a step or hook is between that check and starting its git step or Job does not stop that one piece of work; the next one holds.

While the Pipeline is paused it has a `Paused` condition. `True` (reason `FreezeGateActive`) means the freeze gate holds new promotions. Do not name your own gate `freeze-<pipeline>`: kardinal does not treat a gate it did not create (no `kardinal.io/freeze=true` label and not owned by the Pipeline) as a pause, and does not delete it. While such a gate exists, `kardinal pause` fails with an error naming it, and the condition is `False` with reason `FreezeGateNameConflict`, so the pipeline keeps running. Rename or delete that gate and the pause takes effect.

## Who paused, and why

Since v0.12.0, each pause and resume, of a Pipeline or of one fleet, is an AuditEvent: `PipelinePaused` or
`PipelineResumed`, naming who and why (`--reason`, optional, on `pause` and `resume`) and their
verified actor ([Audit logging](guides/security.md#audit-logging)). kardinal records who in the
same write as the pause (`spec.lastChange`), and the chart's admission policies admit only your
own name there. A pause made another way, such as `kubectl patch` of `spec.paused`, is recorded
too, without an actor: the Kubernetes audit log names who made it.

```bash
kubectl get auditevents -n <namespace> -l kardinal.io/pipeline=my-app
```

## Pause one fleet

A [fleet](pipeline-reference.md#fleets) can be paused on its own, the rest of the Pipeline
promoting as usual:

```bash
# No further target of prod starts; the targets in flight finish
kardinal pause my-app --env prod

# The rollout goes on, in target order, as places free up
kardinal resume my-app --env prod
```

`kardinal pause --env <fleet>` (and **Pause fleet** on the fleet's station in the UI) adds the fleet
to the Pipeline's `spec.pausedFleets`. It works the way [`maxUnavailable`](pipeline-reference.md#fleets)
stops a rollout, not the way a Pipeline pause holds steps:

- No target of the fleet starts, for any version, the rollback of the fleet included.
- Targets already started finish: a target waiting for its PR merge or its health check ends
  `Verified` or `Failed`, so a wave in flight completes.
- Environments after the fleet wait, as they wait for any target that is not Verified.

The controller rebuilds the Graph of every version in flight when the list changes, so a pause
and a resume take effect within one reconcile. `kardinal get pipelines` shows the fleet's column
with `paused`, and `status.summary` starts with `Fleet prod paused;`. A fleet target
(`prod-eu-west`) or an environment that is not a fleet is refused. Changing `spec.pausedFleets`
needs update on `pipelines/pause`, as `spec.paused` does.

A paused fleet cannot be renamed, removed or made a plain environment while it is paused: the
chart's `<release>-fleets` admission policy refuses a Pipeline whose `spec.pausedFleets` names an
environment without a fleet, and says to resume first. Without the policy (the CRDs applied
alone) the pause fails closed: while such an entry is there, every fleet of the Pipeline is
paused, an environment it names is held, and the Pipeline is `Ready=False` naming the entry,
until `kardinal resume <pipeline> --env <old name>` removes it.
