# Rollback

In kardinal-promoter, rollback is not a special operation. It is a forward promotion of a previous Bundle version through the same pipeline, same PolicyGate evaluation, and same PR flow.

## How Rollback Works

1. `kardinal rollback <pipeline> --env <environment>` picks the target: the most recent Bundle, other than the one deployed in that environment now, that was Verified there and deploys different artifacts. A Bundle that an earlier rollback in that environment rolled back from is skipped, so after `v2` was rolled back to `v1`, the next rollback does not return to `v2`. The UI Rollback button, `onHealthFailure: rollback` and RollbackPolicy use the same selection.
2. It creates a new Bundle that copies the target's `spec.images` and `spec.configRef`, with `spec.provenance.rollbackOf` set to the target, `spec.intent.targetEnvironment` set to the environment, the label `kardinal.io/rollback: "true"` and the annotations `kardinal.io/rollback-from: <bundle deployed now>` and `kardinal.io/requested-by: <who ran the rollback>`. The rest of `spec.provenance` is the target's, so its author is the author of the restored build, not the person who rolled back. It also puts back what the deployed Bundle changed that the target does not name (see [Images the target does not name](#images-the-target-does-not-name)).
3. This Bundle runs through the normal promotion flow: Graph generation, PolicyGate evaluation, Git write, PR creation (for pr-review environments), health verification. Like any Bundle with `intent.targetEnvironment`, it is promoted through every environment upstream of the target first (see [Multi-Environment Rollback](#multi-environment-rollback)).
4. The PR title is `[kardinal] Rollback <environment> to <rollback bundle> (restores <target>)`, where `<target>` is the version the rollback deploys: the image tag (for example `(restores 1.28.0)`), `<image>:<tag>` for each of several images, `config <commit>` for a config Bundle, or both for a mixed Bundle (`(restores 1.28.0 with config 0123abc)`). It is the target Bundle's name when the Bundle has no images or config commit. The PR body says which Bundle and version it replaces, which it restores, and who rolled back (see [PR Evidence](pr-evidence.md)). The PR is labeled with `kardinal/rollback` in addition to `kardinal` and `kardinal/promotion`, so filter on `kardinal/rollback` to find rollback PRs. Bitbucket Cloud has no PR labels: there, find rollback PRs by their `[kardinal] Rollback` title (see [SCM providers](scm-providers.md)).

If there is nothing safe to roll back to (no earlier Verified Bundle, or only ones with the same artifacts as the failing Bundle), the command fails and creates nothing. The failing image is never promoted again.

A [rejected](#reject-a-bundle) Bundle is never a target, and never the source of an image or config commit a rollback restores, even when it was Verified in the environment before it was rejected. Neither is any other Bundle that carries one of its artifacts (the same image digest or tag, or the same config commit). `--to` such a Bundle is refused.

### Images the target does not name

A Bundle does not have to name every image of the application. If the deployed Bundle `v2` names only `b:2`, and the target `v1` names only `a:1`, copying `v1` would leave `b:2`, the failing version, in place. So for each image repository the deployed Bundle names and the target does not, the rollback Bundle also carries the version from the newest Bundle that was Verified in the environment, other than the deployed one and the ones an earlier rollback rolled back from.

For example, with `v0 = {a:0, b:0}`, `v1 = {a:1}` and `v2 = {b:2}` all promoted to prod, rolling back `v2` gives `{a:1, b:0}`.

If no such Bundle names the image, the rollback is refused, and the error names the image. `onHealthFailure: rollback` then stops the step at `AbortedByAlarm` for a human. Roll back with `--to` a Bundle that names the image, or promote a fixed version.

The same rule applies to config commits. A config Bundle deploys its config commit, and a `mixed` Bundle deploys its config commit and then its images (see [config-only promotions](design/09-config-only-promotions.md)). When the deployed Bundle is a config or mixed Bundle, the rollback carries the target's config commit or, if the target has none, the newest earlier Verified one, from a config or mixed Bundle. So rolling back a mixed Bundle restores both its images and its config commit.

The rollback Bundle has the target's type, with the exceptions below, and deploys what that type deploys: images for an image Bundle, the config commit for a config Bundle, both for a mixed Bundle. Without `--to`, the target is a Bundle that deploys what the deployed Bundle's type deploys, so the rollback puts back the newest earlier version of what the deployed Bundle changed, whichever Bundle deployed it:

- When an image Bundle is deployed, the target is an image or mixed Bundle. If it is mixed, the rollback Bundle is an image Bundle with only the target's images: the deployed Bundle did not change the config commit, so the config commit stays as deployed. For example, with `v0 = {a:0}`, `m1 = {a:1, config c1}` and `v2 = {a:2}` promoted to prod, rolling back `v2` targets `m1` and deploys the image Bundle `{a:1}`, and prod keeps `c1`.
- When a config Bundle is deployed, the target is a config or mixed Bundle. If it is mixed, the rollback Bundle is a config Bundle with only the target's config commit, and the images stay as deployed.
- When a mixed Bundle is deployed, the target can be a Bundle of any type, so the rollback puts back the newest earlier images and config commit, whichever Bundles deployed them. If the target's type cannot deploy everything the mixed Bundle changed, the rollback Bundle is mixed: it carries the target's artifacts and the newest earlier version of the rest. For example, with `m1 = {a:1, config c1}`, `v2 = {a:2}` and `m3 = {a:3, config c3}` promoted to prod, rolling back `m3` targets `v2` and deploys the mixed Bundle `{a:2, config c1}`. If `m3` had kept `c1`, the rollback would be the image Bundle `{a:2}`.

In each case `spec.provenance.rollbackOf` names the target. The automatic rollbacks choose the same way.

The table below shows, for each type of deployed Bundle, which Bundle a rollback without `--to` targets and what a rollback to each type of target restores. A `--to` target that cannot deploy what the deployed Bundle changed is refused, and the error names what it cannot restore:

| Deployed | Without `--to` | `--to` an image Bundle | `--to` a config Bundle | `--to` a mixed Bundle |
|---|---|---|---|---|
| image | the newest earlier image or mixed Bundle; restores only the images | restores the images | refused | restores the images, and deploys the target's config commit |
| config | the newest earlier config or mixed Bundle; restores only the config commit | refused | restores the config commit | restores the config commit, and deploys the target's images |
| mixed | the newest earlier Bundle of any type; restores the images and the config commit | restores the images; refused if the mixed Bundle changed the config commit (its commit is not the newest earlier Verified one) | restores the config commit; refused if the mixed Bundle changed an image (one of its images is not the newest earlier Verified version) | restores both |

A target whose rollback Bundle, with the added images or config commit, would change nothing the environment runs is skipped, and `--to` such a target fails. Only what the rollback Bundle deploys is compared. The environment runs the deployed Bundle's images and config commit and, for what the deployed Bundle does not deploy (an image it does not name, or the config commit under an image Bundle), the newest earlier Verified version. For example, with the mixed Bundle `m1 = {a:2, config c1}` and then the image Bundle `v2 = {a:2}` promoted, `--to m1` is refused: prod already runs `a:2` and `c1`. Without `--to`, rolling back `v2` skips `m1` whatever its config commit: the image rollback to `m1` would deploy only `a:2`, which prod runs.

`onHealthFailure: rollback` and RollbackPolicy never roll back a Bundle that is itself a rollback. If a rollback fails its health check, the step stops at `AbortedByAlarm` for a human, instead of starting another rollback.

There is no separate rollback subsystem. The same code path handles promotions and rollbacks.

### A config commit that pins images

`config-merge` copies the environment's directory from the config commit over the same directory in the GitOps repo, file by file (see [config-only promotions](design/09-config-only-promotions.md#config-merge-step)). If that directory in the config commit has a kustomization file (`kustomization.yaml`, `kustomization.yml` or `Kustomization`), the copy replaces the GitOps repo's file, and with it the `images:` list where the image steps write each Bundle's tags. The environment then runs the images that the config commit's kustomization file pins, or, if it pins none, the images named in the manifests. This always happens when the config commit comes from the Pipeline's own repository (`configRef.gitRepo` not set): every commit there has the environment's kustomization file, with the images it had at that commit. With `update.strategy: helm`, the same applies to the values file.

The rollback type model does not see this. It assumes a config Bundle deploys only its config commit, and that the images stay those of the newest earlier Bundle that deployed images. What you see:

- The config Bundle's PR or commit changes the `images:` list, and the workload restarts with the pinned images. A config Bundle names no images, so its health check does not check which images run.
- A rollback to an earlier config commit puts back that commit's pins, so it can change the images too, although it is a config Bundle. Rollback chooses its target from what Bundles deploy, not from the files. So it can skip or refuse a target as changing nothing, or accept `--to` a config Bundle under a mixed Bundle that kept the images, while the files change the images.
- A mixed Bundle writes its images after the merge, so the images it names win over the pins. Only the images it does not name take the pinned versions.

What to do:

- Keep image pins out of config commits. Leave the kustomization file (or the Helm values file) out of the config repository's environment directory, and put the config in the files it references: manifests, patches, generator inputs. `config-merge` never deletes a file, so the GitOps repo's kustomization file stays. Use a config repository other than the Pipeline's own.
- Or promote a config change as a mixed Bundle that names every image the environment runs. Its images are written after the merge.
- If an environment already runs the pinned images, promote a new image Bundle with the images it should run (`kardinal create bundle <pipeline> --image <ref>`). Read the `images:` diff of a config Bundle's PR before you merge it.

## CLI

### Roll back to the previous version

```bash
kardinal rollback my-app --env prod
```

Output:
```
Rolling back my-app in prod from my-app-v1-29-0 to my-app-v1-28-0 (ghcr.io/myorg/my-app:v1.28.0)
Bundle my-app-rollback-x7k2p created (rollbackOf=my-app-v1-28-0)
Track with: kardinal explain my-app --env prod
```

### Roll back to a specific version

```bash
kardinal rollback my-app --env prod --to my-app-v1-27-0
```

The `--to` flag names the Bundle to roll back to. It must exist in the Bundle history (within `historyLimit`), belong to the pipeline, carry images or a config ref, differ from what is deployed now, and have been Verified in the environment; otherwise the command fails and creates nothing. `--to` can name a Bundle that an earlier rollback rolled back from. Images the deployed Bundle changed and the `--to` Bundle does not name are filled in the same way as without `--to`.

`--emergency` is deprecated and has no effect. It never bypassed a gate. It prints a warning, the rollback runs as without it, and the flag will be removed in the next minor release. To let a rollback through a blocking gate, use [`kardinal override`](#rollback-and-policygates).

## Automatic Rollback

Automatic rollback is configured per environment with `onHealthFailure`:

```yaml
spec:
  environments:
    - name: prod
      bake:
        minutes: 30
        policy: fail-on-alarm
      onHealthFailure: rollback   # rollback | abort | none (default)
```

| `onHealthFailure` | What happens to the PromotionStep |
|---|---|
| `none` (default) | `Failed`; downstream environments stop |
| `abort` | `AbortedByAlarm`; a human must intervene |
| `rollback` | A rollback Bundle is created with the artifacts of the Bundle verified before the failing one in this environment (the selection in [How Rollback Works](#how-rollback-works)), and the step moves to `RollingBack`. With nothing safe to roll back to, including an image of the failing Bundle that no earlier Bundle names, the step is `AbortedByAlarm` and nothing is created |

**When it applies.** The controller applies `onHealthFailure` when:

- the PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, or of a bake window stopping;
- the health adapter reports a terminal result (Deployment `ProgressDeadlineExceeded` from this promotion's rollout, Flagger canary `Failed`);
- a health check is Unhealthy during a bake window with `bake.policy: fail-on-alarm`.

A rollback Bundle (label `kardinal.io/rollback: "true"` or `spec.provenance.rollbackOf` set) is not rolled back again: when its own health check fails with `rollback` set, the step is `AbortedByAlarm` instead, so rollbacks do not chain.

A rollback of a stalled Deployment returns it to the ReplicaSet it ran before, so Kubernetes creates no new ReplicaSet and keeps the stalled rollout's `ProgressDeadlineExceeded` until it sees the stalled pods go. The rollback Bundle's health check does not fail on that condition: it names the stalled ReplicaSet, which no longer has the Deployment's revision, so the check is Waiting (`... is from an earlier rollout ...`) until Kubernetes replaces it (see [resource](health-adapters.md#adapter-resource-default)). With `health.type: flux`, Flux fails on that condition and checks again only at its next reconcile; the step waits for that (see [flux](health-adapters.md#adapter-flux)).

The rollback Bundle is a newer Bundle of the same pipeline and type, so the failing Bundle ends `Superseded`. When the controller sees the step turn `RollingBack` before it sees the rollback Bundle, the failing Bundle is `Failed` for a moment and then `Superseded`: a Bundle whose only failing environments are `RollingBack` yields to a newer Bundle. The step that raised the alarm stays `RollingBack`, with the message naming the rollback Bundle: it is not cancelled as superseded, whichever order the two status updates land in.

### What counts as a failed health check

Each health check has one of four results (see [Timings and failures](health-adapters.md#timings-and-failures)):

| Result | Examples | Counts in `consecutiveHealthFailures` | Applies `onHealthFailure` |
|---|---|---|---|
| Healthy | The promoted revision runs and is available | No (resets it to 0) | No |
| Waiting | A rollout in progress, including new pods that are not available yet; a Deployment `ProgressDeadlineExceeded` from an earlier rollout; Argo CD or Flux not synced to the promoted commit yet, also when Argo CD is `Degraded` or Flux `Ready=False` on another commit while no Deployment runs the Bundle images (a fix-forward after a failed release); a Rollout or Canary target not on the Bundle images yet, so a `Healthy`, `Degraded`, `Succeeded` or `Failed` phase left by the previous release; Argo Rollouts `Progressing` or `Paused`; Flagger `Progressing` | No | No |
| Unhealthy | A Deployment whose rollout finished but whose pods became unavailable; Argo CD `Degraded` or Flux `Ready=False` once the promoted change is deployed; Argo Rollouts `Degraded` on the promoted revision; target not found | Yes, once per check | Only during a bake window with `fail-on-alarm` |
| Failed | Deployment `ProgressDeadlineExceeded` from this promotion's rollout; Flagger canary `Failed` on the promoted revision | Yes | Yes, at once |

### Health timeout

If a PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, the timeout is a health failure: it increments `status.consecutiveHealthFailures` and applies `onHealthFailure`, as a Failed result does. The step message is `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: <last check>`. The timeout does not apply while a `bake` window runs. When the window stops (an Unhealthy check, or a Waiting one), the timeout starts again from that moment: a release that does not become healthy again fails at the timeout under either `bake.policy`.

A new image whose pods crash-loop is the common case. Kubernetes reports such a Deployment as still rolling out (`Progressing=True`, reason `ReplicaSetUpdated`) until its `progressDeadlineSeconds` (default: 600s) passes, so every check before that is Waiting. The step then fails at `health.timeout`, or earlier when `ProgressDeadlineExceeded` is reported first. To fail sooner, set the Deployment's `progressDeadlineSeconds` below `health.timeout`.

### Delegation failure

A Flagger canary `Failed` on the promoted revision is a Failed result: `onHealthFailure` applies at once. An Argo Rollouts `Degraded` phase on the promoted revision is Unhealthy: each check counts, and `onHealthFailure` applies at the health timeout, or sooner under `bake.policy: fail-on-alarm`. A phase the previous release left (Flagger keeps it until its next analysis tick notices the change) is Waiting: it neither fails nor verifies the Bundle. That includes a `Failed` of the previous release that lands after the health check started but before the GitOps tool applied the Bundle: a Flagger `Failed` fails the step only when Flagger set it after kardinal first saw the target on the Bundle images (`status.targetUpdatedAt`). If kardinal does not see the target updated before Flagger fails the Bundle (the controller was down meanwhile), the step fails at `health.timeout` instead of at once. See [argoRollouts](health-adapters.md#adapter-argorollouts) and [flagger](health-adapters.md#adapter-flagger).

In all cases, the Graph stops all downstream nodes automatically (Graph does not advance past a Failed node). Use `kardinal rollback` to roll back manually.

### `autoRollback` is not implemented

`spec.environments[].autoRollback.failureThreshold` (roll back after N consecutive failed health checks) is reserved. The API server rejects a Pipeline that sets it, with the message `environments[].autoRollback is not implemented`. Remove the field and use `onHealthFailure`.

The `RollbackPolicy` CRD is the building block for that feature. The controller reconciles RollbackPolicy objects that you create yourself, but nothing creates them automatically. A RollbackPolicy reads only the PromotionSteps of its `spec.bundleRef` in `spec.environment`. When the highest `status.consecutiveHealthFailures` among them reaches `spec.failureThreshold` (default: 3), it creates one rollback Bundle.

When the threshold is reached but there is nothing safe to roll back to (see [How Rollback Works](#how-rollback-works)), the RollbackPolicy creates nothing. It sets the `RollbackRefused` condition to `True`, and the message gives the reason. It also emits one `Warning` Event with reason `RollbackRefused`. The condition shows in the `REFUSED` column:

```bash
kubectl get rollbackpolicy
# NAME   SHOULDROLLBACK   FAILURES   THRESHOLD   REFUSED   AGE
# rp-1   true             3          3           True      2m
kubectl get rollbackpolicy rp-1 -o jsonpath='{.status.conditions[?(@.type=="RollbackRefused")].message}'
```

Roll back by hand (see [CLI](#cli)) or fix the policy. The RollbackPolicy is evaluated again when its PromotionSteps or its own spec change; when that evaluation creates the rollback Bundle, the condition becomes `False` with reason `RollbackCreated`. When the failures drop below the threshold first, it becomes `False` with reason `BelowThreshold`.

`SHOULDROLLBACK` follows the failures until a rollback Bundle exists: when they drop below the threshold first (the health check passed again after a refusal), it is `false` again and nothing is created. Once the policy has created a rollback Bundle it stays `true`, and the policy does nothing more. That holds also when the controller stopped between creating the Bundle and writing `status.rollbackBundleName`: the next evaluation finds the Bundle and writes it, whatever the failures are then. `THRESHOLD` shows `3` when `spec.failureThreshold` is not set.

## What Happens in Git

Rollback is a forward promotion. The controller writes the previous version's image tag to the environment's manifests, commits, and pushes (or opens a PR). The Git history shows:

```
commit abc123  [kardinal] Promote my-app-9tptr to prod
commit def456  [kardinal] Promote my-app-rollback-bkgwk to prod
```

Every commit, a rollback's included, is titled `[kardinal] Promote <bundle> to <environment>`, with
the Bundle and Pipeline names in the body. A rollback from the CLI or UI is named
`<pipeline>-rollback-<suffix>`. An automatic one is named `<failing bundle>-rollback-alarm`
(`onHealthFailure: rollback`) or `<bundle>-rollback-policy` (RollbackPolicy); a name longer than
63 characters is shortened and ends in a hash. Every rollback PR is labelled `kardinal/rollback`.
Bitbucket Cloud has no PR labels, so there the PR is titled `[kardinal] Rollback ...`.

The rollback commit is a new commit, not a `git revert`. The history is always append-only.

## Rollback and PolicyGates

Rollback Bundles go through the same PolicyGate evaluation as forward promotions. If the `no-weekend-deploys` gate is active and it is a weekend, the rollback PR will also be blocked.

Rollback Bundles have no gate exemption. A skip-permission gate does not help: it only lets a Bundle skip an environment through `intent.skipEnvironments`, and a rollback does not skip the environment it rolls back.

To let a rollback through a blocking gate, use a break-glass override. It is time-limited and recorded in the audit trail:

```bash
kardinal override my-app --stage prod --gate no-weekend-deploys \
  --reason "Rollback of v1.29.0 — incident #4521" --expires-in 1h
```

See [Emergency Overrides](policy-gates.md#emergency-overrides-k-09).

## Rollback History

The `kardinal history` command shows both promotions and rollbacks:

```bash
kardinal history my-app
```

```
BUNDLE                  ACTION     ENV    PR     DURATION   TIMESTAMP
my-app-rollback-x7k2p   rollback   prod   #145   5m         2026-04-09 11:00
my-app-v1-29-0          promote    prod   #144   15m        2026-04-09 10:20
my-app-v1-28-0          promote    prod   #138   12m        2026-04-07 14:00
```

There is one row per PromotionStep, newest first; steps created in the same second are in name order. `--limit` sets how many rows it prints (default 20), and `--env` keeps the rows of one environment. ACTION is `rollback` for a rollback Bundle. PR is `--` in an environment that opened no PR (`approval: auto`). DURATION is the time from the step's creation to Verified, or for a step that stopped (Failed, AbortedByAlarm, RollingBack) to its last completed step, rounded down to whole seconds, minutes, hours or days (`45s`, `5m`, `2h`, `3d`). It is `...` while the step is in flight and `--` when no end time is recorded. TIMESTAMP is when the step was created, in UTC. The history ends at the Bundles `historyLimit` keeps (see below): a deleted Bundle's rows are gone too.

## How Far Back Can You Roll Back

The `historyLimit` field on the Pipeline (default: 50) determines how many Bundles are retained. `kardinal rollback --to <bundle>` can target any Bundle within the history. Bundles beyond the limit are garbage-collected, but the Git PRs remain as the permanent audit trail.

## Multi-Environment Rollback

When a PromotionStep fails, the Graph stops the environments downstream of it. A rollback targets one environment (`spec.intent.targetEnvironment`), but its Graph keeps every environment upstream of the target, the same as any Bundle with a target environment. So the rollback writes the old artifacts to each upstream environment first, opens PRs there for pr-review environments, and waits on their PolicyGates, soak and health checks, before it reaches the target. Environments that are not upstream of the target are not touched.

For example, in a pipeline `dev -> staging -> [prod-us, prod-eu]`, if `prod-us` fails:
- The rollback promotes the old version to `dev`, then `staging`, then `prod-us`. `dev` and `staging` run the old version afterwards too.
- `prod-eu` is not upstream of `prod-us`. It is not rolled back.
- If both prod environments fail, each gets its own rollback Bundle, and each of them goes through `dev` and `staging`.

## Comparison with Other Tools

| Tool | Rollback mechanism |
|---|---|
| kardinal-promoter | Forward promotion of prior Bundle through same pipeline, same gates, same PR flow |
| Kargo | Re-promote prior Freight to the Stage; since v1.11 this pins the Stage so auto-promotion does not roll forward. Kargo Enterprise adds auto-rollback on failed verification (beta) |
| GitOps Promoter | Manual: create a revert PR (a "restore this version" feature is in an open PR) |
| Argo Rollouts | Automatic in-cluster rollback on AnalysisRun failure (no cross-env awareness) |

---

## Reject a Bundle

A Bundle that must never reach another environment, for example a build with a known vulnerability, can be rejected:

```bash
kardinal reject my-app-v1-29-0 --reason "CVE-2026-1234 in the base image"
```

```
Bundle my-app-v1-29-0 rejected by alice@example.com (was Promoting): CVE-2026-1234 in the base image
It is never promoted again; its unfinished steps are cancelled.
An environment that already runs it keeps it: roll back with kardinal rollback my-app --env <env>.
```

`kardinal reject` writes `spec.rejected` on the Bundle: the reason, the time and your Kubernetes username, which the CLI reads from the API server (a SelfSubjectReview, the call `kubectl auth whoami` makes). The chart's ValidatingAdmissionPolicy admits a rejection only when `spec.rejected.by` is the requesting user, so a rejection always names who made it (see [Verified identity](guides/security.md#verified-identity)). That check is the admission policy's: the CRD alone does not check `by`, so a cluster where the kardinal CRDs are installed without the chart's policy (`kubectl apply -f config/crd/bases`) records whatever name the writer puts there. Rejecting is final: the CRD refuses to change or remove `spec.rejected`.

What happens, whatever phase the Bundle was in (Verified and Superseded included):

- The Bundle turns `Rejected`. Its `Ready` condition is `False` with reason `Rejected`, and its `Rejected` condition names who rejected it and why. A Warning Event `Rejected` is emitted.
- No new PromotionStep is created for it: every step of the Bundle's Graph holds on the Bundle phase, the same hold a Superseded Bundle gets. The Graph and the existing steps stay, as the record of what ran.
- Its steps that have not delivered the change (Pending, Promoting, or WaitingForMerge with the PR still open) fail with `bundle <name> was rejected — promotion cancelled` (or `... rejected before this step started` for a step that never started), and an open PR is closed with a comment naming who rejected the Bundle. A started step writes a `PromotionRejected` AuditEvent.
- A step that already delivered the change keeps going: a HealthChecking step keeps checking health, and a WaitingForMerge step whose PR merged before the rejection moves to HealthChecking as any merged step does. The change is live in that environment, so it is health-checked (`onHealthFailure` applies) and counts as what the environment runs; reject does not revert it.
- A rejection is about the artifacts. `kardinal rollback`, the UI Rollback button, `onHealthFailure: rollback` and RollbackPolicy never roll back to the Bundle, or to any Bundle that carries one of its images or its config commit, and `kardinal promote` never copies one. A Subscription that sees one of those artifacts again creates no Bundle for it: its status message names the rejected Bundle. A Bundle created another way (CI, the CLI, the Bundle API) that carries one, and has not finished promoting, turns `Rejected` too, with the condition reason `RejectedArtifact` naming the rejected Bundle; its `spec.rejected` stays unset.
- An image is matched by digest when the rejected Bundle's image has one: rejecting `r/app:latest@sha256:bad` blocks that digest under any tag, and not a fixed image pushed later under the same moving tag (`r/app:latest@sha256:fixed`). A rejected image without a digest is known only by its tag, so it blocks every image with that repository and tag.
- A rejected Bundle supersedes nothing, and rejecting it does not bring back an older Bundle it already superseded: create a new Bundle to promote again.

To take a rejected Bundle out of an environment that already runs it, roll that environment back. `historyLimit` never deletes a rejected Bundle (`spec.rejected`), whatever its phase, and does not count it: it is the record that its artifacts must not be promoted again (delete it by hand to lift the rejection). A Bundle that is `Rejected` only because it carries a rejected artifact (reason `RejectedArtifact`) adds nothing to that record and is history like a Superseded one. `kardinal get bundles --active` hides Rejected Bundles, and the pipeline views (`kardinal get pipelines`, `status`, `get steps`, `logs`, `explain`, the UI) treat them as history, as they treat Superseded ones, except where the rejected change is live: in an environment where a step of the Rejected Bundle is HealthChecking or Verified, that Bundle stays the current one there, marked Rejected (`<bundle>(Rejected)` in `kardinal get pipelines`), with the hint `rejected change is live; roll back (kardinal rollback <pipeline> --env <env>)`, and the Pipeline is `Degraded` until a rollback or a newer Bundle replaces it. Rejecting from the UI is not available yet: the UI writes as the controller, not as you, so the identity policy would refuse it.

## Pause and Resume

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
- New Bundles are still accepted, but their steps wait in `Pending`.

After resume, held steps continue from where they stopped. A held step re-checks the pause every minute, so this takes up to a minute. No re-trigger is needed.

While the Pipeline is paused it has a `Paused` condition. `True` (reason `FreezeGateActive`) means the freeze gate holds new promotions. Do not name your own PolicyGate `freeze-<pipeline>`: kardinal does not treat a gate it did not create (no `kardinal.io/freeze=true` label and not owned by the Pipeline) as a pause, and does not delete it. While such a gate exists, `kardinal pause` fails with an error naming it, and the condition is `False` with reason `FreezeGateNameConflict`, so the pipeline keeps running. Rename or delete that gate and the pause takes effect.

Upgrading: before this release the UI Pause button, and editing `spec.paused` by hand, set the field without creating the freeze gate, so the Pipeline kept promoting. After upgrading, the controller creates the gate for every Pipeline with `spec.paused: true`, and those Pipelines stop. Resume any that should keep running.
