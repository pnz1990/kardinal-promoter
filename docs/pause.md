# Pause and Resume

During an incident, you may want to stop promotions without rolling back.

```bash
# Pause: no new promotion step starts; in-flight steps hold at the next safe point
kardinal pause my-app

# Resume: held steps continue where they stopped
kardinal resume my-app
```

`kardinal pause` (and the UI Pause button) sets `spec.paused: true` on the Pipeline, and the controller keeps a freeze PolicyGate named `freeze-<pipeline>` in the Pipeline's namespace while it is set. While the Pipeline is paused:
- No PromotionStep leaves `Pending`, so no new environment starts promoting.
- A step in `Promoting` (clone, update manifests, commit, open PR) holds before its next git step. Its status message says the pipeline is paused.
- A step in `WaitingForMerge` or `HealthChecking` finishes. Stopping it would leave a merged change unverified. Open PRs stay open; merging one during a pause still deploys it.
- A [pre hook](hooks.md) that has not started its Job waits in `Pending`, and its timeout does not count. Post hooks keep running, like `HealthChecking` steps: their change is already deployed. A hook whose Job is running finishes.
- New Bundles are still accepted, but their steps wait in `Pending`.

After resume, held steps continue from where they stopped. A held step re-checks the pause every minute, so this takes up to a minute. A waiting pre hook starts as soon as the freeze gate is deleted. No re-trigger is needed.

The pause is checked before each step and each pre hook starts its next piece of work, not continuously. A pause that lands while a step or hook is between that check and starting its git step or Job does not stop that one piece of work; the next one holds.

While the Pipeline is paused it has a `Paused` condition. `True` (reason `FreezeGateActive`) means the freeze gate holds new promotions. Do not name your own PolicyGate `freeze-<pipeline>`: kardinal does not treat a gate it did not create (no `kardinal.io/freeze=true` label and not owned by the Pipeline) as a pause, and does not delete it. While such a gate exists, `kardinal pause` fails with an error naming it, and the condition is `False` with reason `FreezeGateNameConflict`, so the pipeline keeps running. Rename or delete that gate and the pause takes effect.
