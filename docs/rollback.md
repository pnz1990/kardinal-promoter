# Rollback

Roll an environment back to the version that ran there before:

```bash
kardinal rollback my-app prod
```

The rollback is a new version that carries the earlier artifacts, and it goes through the same
pipeline, gates and pull request flow as any promotion. In the UI, the environment's Roll back
button does the same.

## How Rollback Works

1. `kardinal rollback <pipeline> --env <environment>` picks the target: the most recent version, other than the one deployed in that environment now, that was Verified there and deploys different artifacts. A version that an earlier rollback in that environment rolled back from is skipped, so after `v2` was rolled back to `v1`, the next rollback does not return to `v2`. The UI Rollback button, `onHealthFailure: rollback` and RollbackPolicy use the same selection.
2. It creates a new version that copies the target's `spec.images` and `spec.configRef`, with `spec.provenance.rollbackOf` set to the target, `spec.intent.targetEnvironment` set to the environment, the label `kardinal.io/rollback: "true"` and the annotations `kardinal.io/rollback-from: <bundle deployed now>` and `kardinal.io/requested-by: <who ran the rollback>`. The rest of `spec.provenance` is the target's, so its author is the author of the restored build, not the person who rolled back. It also puts back what the deployed version changed that the target does not name (see [Images the target does not name](#images-the-target-does-not-name)).
3. This version runs through the normal promotion flow: Graph generation, gate evaluation, Git write, PR creation (for pr-review environments), health verification. Like any version with `intent.targetEnvironment`, it is promoted through every environment upstream of the target first (see [Multi-Environment Rollback](#multi-environment-rollback)).
4. The PR title is `[kardinal] Rollback <environment> to <rollback bundle> (restores <target>)`, where `<target>` is the version the rollback deploys: the image tag (for example `(restores 1.28.0)`), `<image>:<tag>` for each of several images, `config <commit>` for a config Bundle, or both for a mixed Bundle (`(restores 1.28.0 with config 0123abc)`). It is the target Bundle's name when the Bundle has no images or config commit. The PR body says which Bundle and version it replaces, which it restores, and who rolled back (see [PR Evidence](pr-evidence.md)). The PR is labeled with `kardinal/rollback` in addition to `kardinal` and `kardinal/promotion`, so filter on `kardinal/rollback` to find rollback PRs. Bitbucket Cloud has no PR labels: there, find rollback PRs by their `[kardinal] Rollback` title (see [SCM providers](scm-providers.md)).

If there is nothing safe to roll back to (no earlier Verified version, or only ones with the same artifacts as the failing version), the command fails and creates nothing. The failing image is never promoted again.

A [rejected](reject.md) version is never a target, and never the source of an image or config commit a rollback restores, even when it was Verified in the environment before it was rejected. Neither is any other version that carries one of its artifacts (the same image digest or tag, or the same config commit). `--to` such a version is refused.

### Images the target does not name

A Bundle does not have to name every image of the application. If the deployed Bundle `v2` names only `b:2`, and the target `v1` names only `a:1`, copying `v1` would leave `b:2`, the failing version, in place. So for each image repository the deployed Bundle names and the target does not, the rollback Bundle also carries the version from the newest Bundle that was Verified in the environment, other than the deployed one and the ones an earlier rollback rolled back from.

For example, with `v0 = {a:0, b:0}`, `v1 = {a:1}` and `v2 = {b:2}` all promoted to prod, rolling back `v2` gives `{a:1, b:0}`.

If no such version names the image, the rollback is refused, and the error names the image. `onHealthFailure: rollback` then stops the step at `AbortedByAlarm` for a human. Roll back with `--to` a version that names the image, or promote a fixed version.

The same rule applies to config commits. A config version deploys its config commit, and a `mixed` version deploys its config commit and then its images (see [config-only promotions](design/09-config-only-promotions.md)). When the deployed version is a config or mixed version, the rollback carries the target's config commit or, if the target has none, the newest earlier Verified one, from a config or mixed version. So rolling back a mixed version restores both its images and its config commit.

The rollback Bundle has the target's type, with the exceptions below, and deploys what that type deploys: images for an image Bundle, the config commit for a config Bundle, both for a mixed Bundle. Without `--to`, the target is a Bundle that deploys what the deployed Bundle's type deploys, so the rollback puts back the newest earlier version of what the deployed Bundle changed, whichever Bundle deployed it:

- When an image version is deployed, the target is an image or mixed version. If it is mixed, the rollback version is an image version with only the target's images: the deployed version did not change the config commit, so the config commit stays as deployed. For example, with `v0 = {a:0}`, `m1 = {a:1, config c1}` and `v2 = {a:2}` promoted to prod, rolling back `v2` targets `m1` and deploys the image version `{a:1}`, and prod keeps `c1`.
- When a config version is deployed, the target is a config or mixed version. If it is mixed, the rollback version is a config version with only the target's config commit, and the images stay as deployed.
- When a mixed version is deployed, the target can be a version of any type, so the rollback puts back the newest earlier images and config commit, whichever versions deployed them. If the target's type cannot deploy everything the mixed version changed, the rollback version is mixed: it carries the target's artifacts and the newest earlier version of the rest. For example, with `m1 = {a:1, config c1}`, `v2 = {a:2}` and `m3 = {a:3, config c3}` promoted to prod, rolling back `m3` targets `v2` and deploys the mixed version `{a:2, config c1}`. If `m3` had kept `c1`, the rollback would be the image version `{a:2}`.

In each case `spec.provenance.rollbackOf` names the target. The automatic rollbacks choose the same way.

The table below shows, for each type of deployed version, which version a rollback without `--to` targets and what a rollback to each type of target restores. A `--to` target that cannot deploy what the deployed version changed is refused, and the error names what it cannot restore:

| Deployed | Without `--to` | `--to` a version of type image | `--to` a version of type config | `--to` a version of type mixed |
|---|---|---|---|---|
| image | the newest earlier image or mixed version; restores only the images | restores the images | refused | restores the images, and deploys the target's config commit |
| config | the newest earlier config or mixed version; restores only the config commit | refused | restores the config commit | restores the config commit, and deploys the target's images |
| mixed | the newest earlier version of any type; restores the images and the config commit | restores the images; refused if the mixed version changed the config commit (its commit is not the newest earlier Verified one) | restores the config commit; refused if the mixed version changed an image (one of its images is not the newest earlier Verified version) | restores both |

A target whose rollback version, with the added images or config commit, would change nothing the environment runs is skipped, and `--to` such a target fails. Only what the rollback version deploys is compared. The environment runs the deployed version's images and config commit and, for what the deployed version does not deploy (an image it does not name, or the config commit under an image version), the newest earlier Verified version. For example, with the mixed version `m1 = {a:2, config c1}` and then the image version `v2 = {a:2}` promoted, `--to m1` is refused: prod already runs `a:2` and `c1`. Without `--to`, rolling back `v2` skips `m1` whatever its config commit: the image rollback to `m1` would deploy only `a:2`, which prod runs.

`onHealthFailure: rollback` and RollbackPolicy never roll back a version that is itself a rollback. If a rollback fails its health check, the step stops at `AbortedByAlarm` for a human, instead of starting another rollback.

There is no separate rollback subsystem. The same code path handles promotions and rollbacks.

### A config commit that pins images

`config-merge` copies the environment's directory from the config commit over the same directory in the GitOps repo, file by file (see [config-only promotions](design/09-config-only-promotions.md#config-merge-step)). If that directory in the config commit has a kustomization file (`kustomization.yaml`, `kustomization.yml` or `Kustomization`), the copy replaces the GitOps repo's file, and with it the `images:` list where the image steps write each version's tags. The environment then runs the images that the config commit's kustomization file pins, or, if it pins none, the images named in the manifests. This always happens when the config commit comes from the Pipeline's own repository (`configRef.gitRepo` not set): every commit there has the environment's kustomization file, with the images it had at that commit. With `update.strategy: helm`, the same applies to the values file.

The rollback type model does not see this. It assumes a config version deploys only its config commit, and that the images stay those of the newest earlier version that deployed images. What you see:

- The config version's PR or commit changes the `images:` list, and the workload restarts with the pinned images. A config version names no images, so its health check does not check which images run.
- A rollback to an earlier config commit puts back that commit's pins, so it can change the images too, although it is a config version. Rollback chooses its target from what versions deploy, not from the files. So it can skip or refuse a target as changing nothing, or accept `--to` a config version under a mixed version that kept the images, while the files change the images.
- A mixed version writes its images after the merge, so the images it names win over the pins. Only the images it does not name take the pinned versions.

What to do:

- Keep image pins out of config commits. Leave the kustomization file (or the Helm values file) out of the config repository's environment directory, and put the config in the files it references: manifests, patches, generator inputs. `config-merge` never deletes a file, so the GitOps repo's kustomization file stays. Use a config repository other than the Pipeline's own.
- Or promote a config change as a mixed version that names every image the environment runs. Its images are written after the merge.
- If an environment already runs the pinned images, promote a new image version with the images it should run (`kardinal create bundle <pipeline> --image <ref>`). Read the `images:` diff of a config version's PR before you merge it.

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
# or by version: the one Bundle of my-app that ships 1.27.0
kardinal rollback my-app --env prod --to 1.27.0
```

The `--to` flag names the version to roll back to. It must exist in the version history (within `historyLimit`), belong to the pipeline, carry images or a config ref, differ from what is deployed now, and have been Verified in the environment; otherwise the command fails and creates nothing. `--to` can name a version that an earlier rollback rolled back from. Images the deployed version changed and the `--to` version does not name are filled in the same way as without `--to`.

### Hold an environment

To keep an environment on what it runs now, with no rollback:

```bash
kardinal hold my-app prod --reason "INC-4521: freeze prod while we investigate"
kardinal unhold my-app prod
```

Add `--expires-in 24h` to end the hold after 24 hours.

[`hold`](reference/cli/kardinal-hold.md) writes the same `spec.holds` entry a rollback hold writes, naming the version deployed in
prod; nothing is rolled back and no version is created. To go back to an earlier version and
keep it there, roll back with `--hold` (below).

Running `hold` again with the same reason changes nothing. It refuses a fleet environment (its
targets can run different versions), an environment that runs nothing yet, and one already held
on another version or for another reason: release that hold first. `unhold` is `release-hold`.

### Roll back and hold

```bash
kardinal rollback my-app prod --hold --reason "INC-4521: v1.29.0 leaks connections"
```

Add `--hold-expires-in 24h` to end the hold after 24 hours.

Output:
```
Rolling back my-app in prod from my-app-v1-29-0 to my-app-v1-28-0 (ghcr.io/myorg/my-app:v1.28.0)
Bundle my-app-rollback-3f9a1c created (rollbackOf=my-app-v1-28-0)
Environment prod held on my-app-rollback-3f9a1c: no other version promotes there until: kardinal unhold my-app prod
Gates of prod that would block the rollback pass as EXEMPT (audited) while the controller verifies it restores what was Verified there.
Track with: kardinal explain my-app --env prod
```

`--hold` restores the environment and keeps it there. Without it, the next version CI creates
promotes over the rollback as soon as its gates pass, which is usually not what you want while
an incident is open. While the environment is held:

- **No other version promotes into it.** Versions created after the rollback still promote to the
  environments before it, then wait. Environments after the held one wait too, since they
  depend on it. A step of another version that already exists in the held environment:
    - in `Pending` or `Promoting` holds before its next git step, with the message
      `environment prod is held on rollback my-app-rollback-3f9a1c (INC-4521: ...) — release with: kardinal release-hold my-app --env prod`;
    - in `WaitingForMerge` is cancelled as supersession cancels one: its PR is closed with a
      comment naming the hold, and the step fails with that message;
    - in `HealthChecking` has already merged, so it finishes. The rollback, a newer version,
      supersedes it.
- **The rollback is never superseded** by a newer version. `historyLimit` does not delete it, its
  `rollbackOf`, or the versions that deployed its artifacts, and they do not count toward the
  limit.
- **The rollback passes the gates of the held environment that would block it**, if the
  controller can verify it. See [what is exempt](policy-gates.md#rollback-hold-exemption).
  The exemption is never silent. The gate's reason starts with
  `EXEMPT: rollback my-app-rollback-3f9a1c holds prod (by alice: INC-4521: ...); without the hold: <the gate's own result>`.
  The flip is recorded as a `GateEvaluated` AuditEvent (`kardinal audit`), and the gate gets a
  `GateExempted` Warning Event. Gates of the environments before the held one are not exempt:
  the rollback passes them as any version does, or through
  [`kardinal override`](#rollback-and-gates). A pause (`kardinal pause`) still holds it.
- **Every hold change is audited.** The controller writes a `HoldCreated` AuditEvent when a hold
  appears and a `HoldReleased` one when it goes, however it was changed: the CLI, the UI or
  `kubectl`.

`--reason` is required with `--hold`. The hold records who held the environment and when.
`kardinal explain my-app --env prod` and the UI show it. An environment holds at most one rollback:
a second `--hold` on the same environment is refused until the first is released.
`--hold-expires-in` ends the hold by itself. From `expiresAt` on, the hold counts as absent: the
exemption ends, steps of other versions are no longer held, and the controller removes the entry
and writes `HoldReleased`.

#### A hold whose rollback Bundle does not exist

The hold is written first, then the rollback version. If the client or the controller stops
between the two, or someone deletes the version later, the hold names a version that does not
exist. **A missing version never lifts a hold**: the hold stays in effect, and no other version
promotes into the environment until a person with `pipelines/hold` releases it.
Deleting a version therefore cannot end a hold.

The controller records each hold's state in the Pipeline's `status.holdStates`: `Active`
(the version exists) or `BundleMissing` (the controller found it missing; `bundleMissingSince`
is the first time it did, by the controller's clock). When the version has been missing for
2 minutes (the controller flag `--hold-bundle-grace`, set with `controller.extraArgs`), the
controller reports it:

- the Pipeline condition `HoldBundleMissing` is `True`, naming the environment, the version and
  the command that releases the hold;
- a `HoldBundleMissing` AuditEvent (outcome `Failure`): the durable record, written once per
  hold through the audit outbox;
- a `Warning` Event with reason `HoldBundleMissing` on the Pipeline, and
  `kardinal_hold_bundle_missing_total{pipeline_namespace,pipeline}` goes up by one
  ([kardinal metrics](reference/metrics.md#kardinal-metrics)), each at most once per hold
  (Events expire, and a controller that stops between the status write and the Event can
  miss them);
- `kardinal explain` and the UI's hold badge show the missing version and the release command.

To recover, release the hold, then roll back and hold again if you still want the
environment held:

```bash
kardinal unhold my-app prod
kardinal rollback my-app --env prod --hold --reason "INC-4521: v1.29.0 leaks connections"
```

The UI's **Release hold** does the first step. A second `--hold` is refused while the first
is in `spec.holds`, missing version or not. If the version exists again, the hold is `Active`
again and the condition is `False`. A hold released and added again is a new hold, with a
report of its own. When the controller cannot read the version (an API error other than not
found), it keeps the hold's state and sets the condition to `Unknown` (reason
`LookupFailed`).

```bash
kubectl get pipeline my-app -o jsonpath='{range .status.holdStates[*]}{.environment}={.state} {.message}{"\n"}{end}'
```

Release the hold when the fix is ready:

```bash
kardinal unhold my-app prod
```

The newest version that was held back then promotes into the environment through its gates as
usual. The rollback's gates are evaluated without the exemption again.

In the UI, the rollback dialog has a **Hold** option with a reason field and an optional end
(4 hours, 24 hours, 3 days). A held environment shows a **Held** badge with the reason and a
**Release hold** action.

#### Who may hold

The hold is the Pipeline field [`spec.holds`](reference/pipeline.md#specholds). The chart
installs the ValidatingAdmissionPolicy `<release>-hold-writes`, which refuses any change to
`spec.holds` unless the caller has `update` on the virtual subresource `pipelines/hold`. Plain
`update` on the Pipeline is not enough. Every new entry must name the caller in `createdBy`
(the CLI reads your Kubernetes user name with a SelfSubjectReview) and have a `createdAt`. An
entry is added or removed, never edited in place. The UI checks `pipelines/hold` for the UI user
before it writes the hold, and records that user in `createdBy`. Grant it like this:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: incident-commander
  namespace: my-team
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["pipelines/hold"]
    verbs: ["update"]
  - apiGroups: ["kardinal.io"]
    resources: ["pipelines"]
    verbs: ["get", "update"]
  - apiGroups: ["kardinal.io"]
    resources: ["bundles"]
    verbs: ["get", "list", "create"]
```

To let a rollback through a blocking gate, use [`kardinal override`](#rollback-and-gates).

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
| `rollback` | A rollback version is created with the artifacts of the version verified before the failing one in this environment (the selection in [How Rollback Works](#how-rollback-works)), and the step moves to `RollingBack`. With nothing safe to roll back to, including an image of the failing version that no earlier version names, the step is `AbortedByAlarm` and nothing is created |

**When it applies.** The controller applies `onHealthFailure` when:

- the PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, or of a bake window stopping;
- the health adapter reports a terminal result (Deployment `ProgressDeadlineExceeded` from this promotion's rollout, Flagger canary `Failed`);
- a health check is Unhealthy during a bake window with `bake.policy: fail-on-alarm`.

A rollback version (label `kardinal.io/rollback: "true"` or `spec.provenance.rollbackOf` set) is not rolled back again: when its own health check fails with `rollback` set, the step is `AbortedByAlarm` instead, so rollbacks do not chain.

A rollback of a stalled Deployment returns it to the ReplicaSet it ran before, so Kubernetes creates no new ReplicaSet and keeps the stalled rollout's `ProgressDeadlineExceeded` until it sees the stalled pods go. The rollback version's health check does not fail on that condition: it names the stalled ReplicaSet, which no longer has the Deployment's revision, so the check is Waiting (`... is from an earlier rollout ...`) until Kubernetes replaces it (see [resource](health-adapters.md#adapter-resource-default)). With `health.type: flux`, Flux fails on that condition and checks again only at its next reconcile; the step waits for that (see [flux](health-adapters.md#adapter-flux)).

The rollback version is a newer version of the same pipeline and type, so the failing version ends `Superseded`. When the controller sees the step turn `RollingBack` before it sees the rollback version, the failing version is `Failed` for a moment and then `Superseded`: a version whose only failing environments are `RollingBack` yields to a newer version. The step that raised the alarm stays `RollingBack`, with the message naming the rollback version: it is not cancelled as superseded, whichever order the two status updates land in.

### What counts as a failed health check

Each health check has one of four results (see [Timings and failures](health-adapters.md#timings-and-failures)):

| Result | Examples | Counts in `consecutiveHealthFailures` | Applies `onHealthFailure` |
|---|---|---|---|
| Healthy | The promoted revision runs and is available | No (resets it to 0) | No |
| Waiting | A rollout in progress, including new pods that are not available yet; a Deployment `ProgressDeadlineExceeded` from an earlier rollout; Argo CD or Flux not synced to the promoted commit yet, also when Argo CD is `Degraded` or Flux `Ready=False` on another commit while no Deployment runs the version images (a fix-forward after a failed release); a Rollout or Canary target not on the version images yet, so a `Healthy`, `Degraded`, `Succeeded` or `Failed` phase left by the previous release; Argo Rollouts `Progressing` or `Paused`; Flagger `Progressing` | No | No |
| Unhealthy | A Deployment whose rollout finished but whose pods became unavailable; Argo CD `Degraded` or Flux `Ready=False` once the promoted change is deployed; Argo Rollouts `Degraded` on the promoted revision; target not found | Yes, once per check | Only during a bake window with `fail-on-alarm` |
| Failed | Deployment `ProgressDeadlineExceeded` from this promotion's rollout; Flagger canary `Failed` on the promoted revision | Yes | Yes, at once |

### Health timeout

If a PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, the timeout is a health failure: it increments `status.consecutiveHealthFailures` and applies `onHealthFailure`, as a Failed result does. The step message is `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: <last check>`. The timeout does not apply while a `bake` window runs. When the window stops (an Unhealthy check, or a Waiting one), the timeout starts again from that moment: a release that does not become healthy again fails at the timeout under either `bake.policy`.

A new image whose pods crash-loop is the common case. Kubernetes reports such a Deployment as still rolling out (`Progressing=True`, reason `ReplicaSetUpdated`) until its `progressDeadlineSeconds` (default: 600s) passes, so every check before that is Waiting. The step then fails at `health.timeout`, or earlier when `ProgressDeadlineExceeded` is reported first. To fail sooner, set the Deployment's `progressDeadlineSeconds` below `health.timeout`.

### Delegation failure

A Flagger canary `Failed` on the promoted revision is a Failed result: `onHealthFailure` applies at once. An Argo Rollouts `Degraded` phase on the promoted revision is Unhealthy: each check counts, and `onHealthFailure` applies at the health timeout, or sooner under `bake.policy: fail-on-alarm`. A phase the previous release left (Flagger keeps it until its next analysis tick notices the change) is Waiting: it neither fails nor verifies the version. That includes a `Failed` of the previous release that lands after the health check started but before the GitOps tool applied the version: a Flagger `Failed` fails the step only when Flagger set it after kardinal first saw the target on the version images (`status.targetUpdatedAt`). If kardinal does not see the target updated before Flagger fails the version (the controller was down meanwhile), the step fails at `health.timeout` instead of at once. See [argoRollouts](health-adapters.md#adapter-argorollouts) and [flagger](health-adapters.md#adapter-flagger).

In all cases, the Graph stops all downstream nodes automatically (Graph does not advance past a Failed node). Use `kardinal rollback` to roll back manually.

### RollbackPolicy

A `RollbackPolicy` rolls back after a number of consecutive failed health checks. The controller reconciles RollbackPolicy objects that you create yourself, but nothing creates them automatically. A RollbackPolicy reads only the PromotionSteps of its `spec.bundleRef` in `spec.environment`. When the highest `status.consecutiveHealthFailures` among them reaches `spec.failureThreshold` (default: 3), it creates one rollback version.

When the threshold is reached but there is nothing safe to roll back to (see [How Rollback Works](#how-rollback-works)), the RollbackPolicy creates nothing. It sets the `RollbackRefused` condition to `True`, and the message gives the reason. It also emits one `Warning` Event with reason `RollbackRefused`. The condition shows in the `REFUSED` column:

```bash
kubectl get rollbackpolicy
# NAME   SHOULDROLLBACK   FAILURES   THRESHOLD   REFUSED   AGE
# rp-1   true             3          3           True      2m
kubectl get rollbackpolicy rp-1 -o jsonpath='{.status.conditions[?(@.type=="RollbackRefused")].message}'
```

Roll back by hand (see [CLI](#cli)) or fix the policy. The RollbackPolicy is evaluated again when its PromotionSteps or its own spec change; when that evaluation creates the rollback version, the condition becomes `False` with reason `RollbackCreated`. When the failures drop below the threshold first, it becomes `False` with reason `BelowThreshold`.

`SHOULDROLLBACK` follows the failures until a rollback version exists: when they drop below the threshold first (the health check passed again after a refusal), it is `false` again and nothing is created. Once the policy has created a rollback version it stays `true`, and the policy does nothing more. That holds also when the controller stopped between creating the version and writing `status.rollbackBundleName`: the next evaluation finds the version and writes it, whatever the failures are then. `THRESHOLD` shows `3` when `spec.failureThreshold` is not set.

## What Happens in Git

Rollback is a forward promotion. The controller writes the previous version's image tag to the environment's manifests, commits, and pushes (or opens a PR). The Git history shows:

```
commit abc123  [kardinal] Promote my-app-9tptr to prod
commit def456  [kardinal] Promote my-app-rollback-bkgwk to prod
```

Every commit, a rollback's included, is titled `[kardinal] Promote <bundle> to <environment>`, with
the version and Pipeline names in the body. A rollback from the CLI or UI is named
`<pipeline>-rollback-<suffix>`. An automatic one is named `<failing bundle>-rollback-alarm`
(`onHealthFailure: rollback`) or `<bundle>-rollback-policy` (RollbackPolicy); a name longer than
63 characters is shortened and ends in a hash. Every rollback PR is labelled `kardinal/rollback`.
Bitbucket Cloud has no PR labels, so there the PR is titled `[kardinal] Rollback ...`.

The rollback commit is a new commit, not a `git revert`. The history is always append-only.

## Rollback and gates

Rollback versions go through the same gate evaluation as forward promotions. If the `no-weekend-deploys` gate is active and it is a weekend, the rollback PR will also be blocked.

A rollback version has no gate exemption unless it was created with `--hold` ([Roll back and hold](#roll-back-and-hold)). Then it passes the gates that would block it, each pass recorded with an `EXEMPT` reason, an AuditEvent and a Warning Event. A skip-permission gate does not help: it only lets a version skip an environment through `intent.skipEnvironments`, and a rollback does not skip the environment it rolls back.

To let a rollback through a blocking gate, use a break-glass override. It is time-limited and recorded in the audit trail:

```bash
kardinal override my-app --stage prod --gate no-weekend-deploys \
  --reason "Rollback of v1.29.0 — incident #4521" --expires-in 1h
```

See [Emergency Overrides](overrides.md).

## Rollback History

The `kardinal history` command shows both promotions and rollbacks:

```bash
kardinal history my-app
```

```
VERSION                          ACTION     ENV    PR     DURATION   TIMESTAMP
1.28.0 (my-app-rollback-x7k2p)   rollback   prod   #145   5m         2026-04-09 11:00
1.29.0 (my-app-v1-29-0)          promote    prod   #144   15m        2026-04-09 10:20
1.28.0 (my-app-v1-28-0)          promote    prod   #138   12m        2026-04-07 14:00
```

There is one row per PromotionStep, newest first; steps created in the same second are in name order. `--limit` sets how many rows it prints (default 20), and `--env` keeps the rows of one environment. ACTION is `rollback` for a rollback version. PR is `--` in an environment that opened no PR (`approval: auto`). DURATION is the time from the step's creation to Verified, or for a step that stopped (Failed, AbortedByAlarm, RollingBack) to its last completed step, rounded down to whole seconds, minutes, hours or days (`45s`, `5m`, `2h`, `3d`). It is `...` while the step is in flight and `--` when no end time is recorded. TIMESTAMP is when the step was created, in UTC. The history ends at the versions `historyLimit` keeps (see below): a deleted version's rows are gone too.

## How Far Back Can You Roll Back

The `historyLimit` field on the Pipeline (default: 50) determines how many versions are retained. `kardinal rollback --to <bundle|version>` can target any version within the history. Versions beyond the limit are garbage-collected, but the Git PRs remain as the permanent audit trail.

## Multi-Environment Rollback

When a PromotionStep fails, the Graph stops the environments downstream of it. A rollback targets one environment (`spec.intent.targetEnvironment`), but its Graph keeps every environment upstream of the target, the same as any version with a target environment. So the rollback writes the old artifacts to each upstream environment first, opens PRs there for pr-review environments, and waits on their gates, soak and health checks, before it reaches the target. Environments that are not upstream of the target are not touched.

For example, in a pipeline `dev -> staging -> [prod-us, prod-eu]`, if `prod-us` fails:
- The rollback promotes the old version to `dev`, then `staging`, then `prod-us`. `dev` and `staging` run the old version afterwards too.
- `prod-eu` is not upstream of `prod-us`. It is not rolled back.
- If both prod environments fail, each gets its own rollback version, and each of them goes through `dev` and `staging`.

To stop a version everywhere, [reject it](reject.md). To stop promotions without rolling back, [pause the pipeline](pause.md).
