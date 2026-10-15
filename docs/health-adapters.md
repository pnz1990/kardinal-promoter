# Health Adapters

After a promotion is applied (manifests written to Git), kardinal-promoter verifies that the target environment is healthy and runs **the promoted revision** before marking the PromotionStep as Verified. `argocd` and `flux` check the promoted commit. `resource`, `argoRollouts` and `flagger` check the version images. A config-only version has no images, and an image renamed by kustomize `newName` cannot be matched. In both cases `resource` cannot tell the new revision from the previous one, `argoRollouts` counts the phase only when it was set after the change reached git, and `flagger` only when it was set after the health check started; see [What "the promoted revision" means](#what-the-promoted-revision-means). Health verification uses pluggable adapters that check the appropriate Kubernetes resource status.

## Selecting an Adapter

```yaml
environments:
  - name: prod
    health:
      type: argocd   # one of resource | argocd | flux | argoRollouts | flagger (default: resource)
```

The adapter is chosen in this order:

1. `delivery.delegate` (`argoRollouts` or `flagger`), when set and not `none`;
2. `health.type`;
3. `resource` when neither is set. There is no probing of the cluster for installed CRDs.

An unknown `health.type` is rejected by the CRD schema when the Pipeline is applied.

## What "the promoted revision" means

The PromotionStep records the git commit its promotion delivered:

- `status.outputs.commitSHA` — the commit pushed straight to the environment branch (`approval: auto`). A step whose `git-commit` found nothing to commit (`status.outputs.noChanges: "true"`, for `auto` and `pr-review` alike) pushed and opened nothing; the branch it cloned already holds the change, so `commitSHA` is that branch's head when the step cloned it, and the health check waits for the GitOps tool to apply it (or a later commit that contains it) like any pushed commit. A step that started before this rule and recorded no commit keeps the old check until it finishes;
- `status.outputs.mergeCommitSHA` — the merge commit of the promotion PR (`approval: pr-review`), copied from `PRStatus.status.mergeCommitSHA` when the step sees the merge, or at the next health check when the PRStatus records it later (a webhook can report the merge before the merge commit is known; see [below](#when-the-merge-commit-is-not-known-yet)).

The `argocd` and `flux` adapters require that commit. On a branch shared with other environments, both also accept another commit when the version images run: for `argocd` in the Application, for `flux` in the Kustomization's Deployments (see below). They compare images, not git history, so they do not check that the other commit is later than the promoted one. The `resource` adapter requires the version images in the Deployment's pod template, `argoRollouts` in the Rollout's, and `flagger` in the Canary's target Deployment and, for `Succeeded`, its primary Deployment. `kubectl get promotionstep <name> -o yaml` shows the recorded commit in `status.outputs` and the last health result in `status.message`.

### When the merge commit is not known yet

The SCM webhook can mark the PR merged before the merge commit is known: GitLab reports none for a fast-forward merge, and kardinal reads none from the Bitbucket and Azure DevOps events. The PRStatus reconciler then asks the SCM provider for it and records `PRStatus.status.mergeCommitSHA`, or `status.mergeCommitUnavailable: true` once it stops asking: at once when the provider cannot report merge commits or answers without one (an Azure DevOps PR without `lastMergeCommit`, a Bitbucket PR whose `merge_commit` is empty) or the lookup fails with an error a retry cannot fix (401, 403 that is not a rate limit, 404, 410), and otherwise when the lookup still fails 5 minutes after the merge. Until one of the two is set, the `argocd` and `flux` checks of a step that opened a PR wait with `merge commit of the PR not known yet`, instead of passing on an Application or Kustomization that is healthy on the previous commit. The wait counts toward `health.timeout`, so a lookup that keeps failing can use up a `health.timeout` of 5 minutes or less. Once `mergeCommitUnavailable` is set, `argocd` checks `status.summary.images` against the version images, and `flux`, which has nothing to fall back on, waits until `health.timeout` (see [flux](#adapter-flux)). A direct push, a step with no changes and `update.strategy: argocd` open no PR and do not wait for a merge commit (a step with no changes waits for the branch head it recorded instead); `resource`, `argoRollouts` and `flagger` check images only and do not wait either.

## Adapter: resource (default)

Checks a Kubernetes Deployment the way `kubectl rollout status` does.

```yaml
health:
  type: resource
  resource:
    kind: Deployment            # the only supported kind
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
    condition: Available        # default
  timeout: 10m
```

**Healthy when**, in order:

1. every container that runs one of the version's image repositories runs the version's tag or digest (otherwise: waiting, "not updated yet");
2. `status.observedGeneration` is at least `metadata.generation` (otherwise: waiting);
3. the `Progressing` condition does not have reason `ProgressDeadlineExceeded` (otherwise: **failed**, see [Timings and failures](#timings-and-failures), or waiting when the condition is from an earlier rollout, see below);
4. all replicas are updated, no old replicas remain, and every updated replica is available (otherwise: waiting while the rollout runs; unhealthy if replicas become unavailable after the rollout finished);
5. the configured condition (default `Available`) is `True`.

While replicas are unavailable, a result that is not healthy names why the first new pod is not ready, for example `; new pod my-app-7d9f-abcde: container app is waiting: ErrImagePull: ...`, `CrashLoopBackOff`, or `is running but not ready (readiness probe)`, so a `health.timeout` message, which quotes the last result, says what failed. The adapter lists the pods of the new ReplicaSet (the one the `Progressing` condition names) with `list` on `pods` (in the chart's RBAC), uncached; without that permission the message leaves the pod out.

If the Deployment runs none of the version's image repositories (for example, kustomize `newName` renamed the image), the image cannot be verified. The check then passes with "(image not verified)" in the message. A version without images (a config-only version) has nothing to compare either. Before the GitOps tool applies the change, the old Deployment is already rolled out and Available, so the check can pass on the previous revision.

A `ProgressDeadlineExceeded` fails the step only when it is a stall of this promotion's rollout. The condition's message names the ReplicaSet that timed out (`ReplicaSet "<name>" has timed out progressing.`), and the adapter reads that ReplicaSet with `get` on `replicasets` (in the chart's RBAC):

- When it has another revision than the Deployment (annotation `deployment.kubernetes.io/revision`), or no longer exists, the condition is from an earlier rollout and the check waits.
- When it has the Deployment's revision, the Deployment's current pod template stalled. For a `type: image` version whose images that template runs, the template is the promoted state, so the step fails at once, also when the stall is older than the health check: a second Deployment of a `labelSelector` that already stalled on the version images, or a first check later than the stall (for example after a controller restart). A `config` or `mixed` version may change the template in ways the check cannot see, and a template on none of the version's repositories may not be updated yet, so for them the condition's time decides, as in the next case. Otherwise the rollback of a broken config change would fail on the broken release's stall before Argo CD applied the rollback.
- When the ReplicaSet cannot tell, because the read fails or the message or a revision is missing, the condition's time decides: it counts only when the Deployment controller set it after the health check started and, when the image can be verified, after a check first found the pod template on the version images. The check records that time in the PromotionStep's `status.targetUpdatedAt`.

An earlier one is Waiting: `Deployment <ns>/<name>: ProgressDeadlineExceeded (<message>) is from an earlier rollout: ...; waiting for the Deployment controller to see this rollout progress`. This happens after a rollback. The rollback returns the Deployment to the ReplicaSet it ran before the stalled release, so the Deployment controller creates no new ReplicaSet and keeps the stalled rollout's condition, with its time and message, until it sees the stalled pods go. The rollback gives that ReplicaSet the next revision, so the stalled one no longer has the Deployment's revision. If the version's rollout stalls again before the controller replaced the earlier condition, the condition does not change: the step fails at `health.timeout` instead of at once. The same holds when the time decides and the stall is older than the health check, for a `config` or `mixed` version or without the ReplicaSet read: it waits until `health.timeout`. A promotion that changed nothing in git, or a step sequence without a `health-check` step, has no health-check start, so the time counts every `ProgressDeadlineExceeded`; a ReplicaSet of another revision still waits.

`resource.kind` other than `Deployment` fails the PromotionStep with `health.resource.kind "StatefulSet" is not supported: only Deployment is checked`. For other workloads, use the `argocd` or `flux` adapter.

**When to use:** Clusters without Argo CD or Flux, or as a baseline when only Kubernetes Deployments are available.

### WatchKind mode (label selector)

By default, the `resource` adapter checks a single named Deployment (`Pipeline.metadata.name`). When `health.labelSelector` is set, it checks **every** Deployment in the namespace (`resource.namespace`, default: the environment name) that matches the selector, and `resource.name` is ignored.

```yaml
health:
  type: resource
  labelSelector:
    app: my-app
    kardinal.io/pipeline: nginx-demo
  timeout: 10m
```

**When to use WatchKind mode:**
- When your Deployment name does not match the Pipeline name
- When you deploy multiple Deployments per environment and need all of them healthy before advancing
- When you want to match Deployments by label rather than by exact name

**How it works:** Each matched Deployment must pass all five checks above. The step reports the worst result: failed, then unhealthy, then waiting. No matching Deployment is unhealthy, not vacuously healthy.

> **Note:** `labelSelector` is only supported for `health.type: resource`. For `argocd`, `flux`, `argoRollouts`, and `flagger`, the adapter always watches a single named resource and `labelSelector` is ignored.

## Adapter: argocd

Watches an Argo CD Application's health, sync, operation status and synced revision.

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod           # default: <pipeline>-<environment>
    namespace: argocd           # default: "argocd"
  timeout: 15m
```

**Healthy when:** all of these are met:
- `status.health.status` = `Healthy`
- `status.sync.status` = `Synced`
- `status.operationState.phase` = `Succeeded` (or no operation recorded, or a finished operation on another commit)
- the Application synced the promoted commit: it appears in `status.sync.revision(s)`, `status.operationState.syncResult.revision(s)` or `status.history`. `Synced` alone only means the cluster matches whatever commit Argo CD last fetched, which can be the previous one.

On a branch shared with other environments, a later commit can reach Argo CD before ours does. When the Application is `Synced` on another revision, the adapter reads the commit graph near the branch head (one `ls-remote` per repository per 30 seconds and one read per branch head, shared by every step that checks at the same time; the last 20 commits, then 500) and accepts the revision when the promoted commit is that revision or one of its ancestors, through every parent, so a merge commit contains both sides: the message says `(synced revision <rev> contains <commit>)`. A later revision counts only when no commit after ours changed the environment's files (its path, and a Helm `valuesFile` or `chartVersionFile` outside it): an older version's push that landed after ours and rewrote them does not. This needs no Pods, so a Deployment scaled to zero or an overlay without workloads passes too. A revision that is not on the branch, a PR's original commits after a squash or rebase merge (only the commits on the branch count), or a promoted commit older than the last 500 commits does not count. Then, or when the history cannot be read (the message says why), the adapter still accepts the revision when `status.summary.images` shows the version images. With `update.strategy: argocd` there is no commit to compare, so `status.summary.images` must show the version images; when it lists none of their repositories, the images are not verified (see below).

**Unhealthy only once the promoted change is deployed.** Until Argo CD has deployed the promoted commit, `Degraded` health and a failed operation describe the version before it, so the check waits instead of counting a failure. The commit is deployed once the Application is `Synced` on it, a finished operation ran on it, or it is in `status.history`. While an operation on the promoted commit still runs (a long PreSync hook in a fix-forward or a rollback, say), `Degraded` health counts only once the Application is `Synced` on the commit or has it in `status.history`. `status.sync.revision` alone does not count: Argo CD sets it to the newest commit it fetched, also while the Application is `OutOfSync` with auto-sync off. An operation counts only when it ran on the promoted commit: `status.operationState.syncResult.revision(s)`, or the requested `operation.sync.revision(s)` before Argo CD records a result. The message then names the operation it ignores, for example `waiting for argocd: health=Healthy, sync=OutOfSync, opPhase=Failed, revision=<new> not synced yet, ignoring the operation on <old>`. With `update.strategy: argocd`, the change is deployed once `status.summary.images` shows the version images. A sync that fails before then (a failed PreSync hook, a manifest the API server rejects) is treated as not deployed yet: the check waits instead of counting a health failure, and the promotion fails when `health.timeout` expires. When `status.summary.images` lists none of the version's image repositories (a kustomize `newName` renames the image, say), the images cannot be verified: a `Healthy`, `Synced` Application still passes, with `(image not verified: runs none of the Bundle images)` in the message, but `Degraded` health and a failed operation wait in the same way.

**When to use:** Any cluster managed by Argo CD. This is the recommended adapter for Argo CD users because it verifies that Argo CD successfully synced the promoted manifests, not just that the Deployment is running.

**Multi-cluster:** In the Argo CD hub-spoke model, all Applications live in the hub cluster. The controller reads Application status from the hub. No cross-cluster API calls needed. See [Remote Clusters](#remote-clusters).

**Edge cases:**
| Application state | Adapter behavior |
|---|---|
| `health.status = Progressing`, `Missing` or `Suspended` | Wait |
| `sync.status = OutOfSync` | Wait (may be mid-sync-wave) |
| Synced to an older revision | Wait (`revision=<old>, waiting for <new>`) |
| The promoted commit fetched but not synced (auto-sync off) | Wait (`revision=<new> not synced yet`) |
| `health.status = Degraded` once the promoted change is deployed | Unhealthy (counts as a health failure) |
| `health.status = Degraded` before that | Wait: it is the previous version's health |
| `operationState.phase = Failed` or `Error` on the promoted commit | Unhealthy (counts as a health failure) |
| `operationState.phase = Failed` or `Error` on another commit | Wait until the promoted commit is synced (`ignoring the operation on <old>`); does not hold a Healthy, Synced promoted commit |
| An operation running on the promoted commit | Wait. `Degraded` health counts only once the promoted commit is `Synced` or in `status.history` |
| An operation running on another commit | Wait. `Degraded` health counts once the promoted commit is deployed, or, on a shared branch, once `status.summary.images` shows the version images |
| An operation running, with `update.strategy: argocd` | Wait. `Degraded` health counts once `status.summary.images` shows the version images |
| `update.strategy: argocd`, and `status.summary.images` lists none of the version repositories | `Healthy` and `Synced` pass (`image not verified`); `Degraded` health or a failed operation waits |
| `approval: pr-review` and the merge commit is not known yet | Wait (`merge commit of the PR not known yet`) while the PRStatus can still learn it; once it records `mergeCommitUnavailable`, `status.summary.images` must show the version images (see [above](#when-the-merge-commit-is-not-known-yet)). `onHealthFailure` at `health.timeout` |
| Application not found | Unhealthy (counts as a health failure) |

Unhealthy results count toward `status.consecutiveHealthFailures`; waiting results do not. When `health.timeout` expires without a Healthy result, whichever of the two the last check returned, the timeout counts as one more health failure and applies `onHealthFailure`. See [Timings and failures](#timings-and-failures).

**Hook Jobs with a fixed name need `HookSucceeded`.** A hook Job with `argocd.argoproj.io/hook-delete-policy: BeforeHookCreation` alone stays after it succeeds, and the next sync deletes it and creates a new Job with the same name. Argo CD can then mark the new operation `Succeeded` from the old Job's `Complete` status while the new Job still runs, so the check passes even when the new Job fails. Use `argocd.argoproj.io/hook-delete-policy: HookSucceeded,BeforeHookCreation`: Argo CD deletes the Job once it succeeds, and the next sync waits for its own Job.

## Adapter: flux

Watches a Flux Kustomization's reconciliation status.

```yaml
health:
  type: flux
  flux:
    name: my-app-prod           # default: <pipeline>-<environment>
    namespace: flux-system       # default: "flux-system"
  timeout: 10m
```

**Healthy when:** all of these are met:
- `Ready=True` in `status.conditions`
- `status.observedGeneration` equals `metadata.generation` (the controller has reconciled the latest spec). A Kustomization missing either field waits.
- `status.lastAppliedRevision` is the promoted commit (Flux reports `<branch>@sha1:<commit>`, or `<branch>/<commit>` before Flux 2.0), or another commit while the Kustomization's Deployments run the version images (see the table below). A revision that is not a git commit (an OCI or Bucket source) cannot be compared: the check passes with "(revision not verified)" in the message.

**When to use:** Any cluster managed by Flux.

**Multi-cluster:** Use a Kustomization in the hub that targets the remote cluster with `spec.kubeConfig.secretRef`; see [Remote Clusters](#remote-clusters).

**Edge cases:**
| Kustomization state | Adapter behavior |
|---|---|
| `Ready=True`, generation matches, promoted commit applied | Healthy |
| `Ready=True`, older commit applied | Wait |
| `Ready=True`, another commit applied (a sibling environment pushed to the same branch before Flux fetched ours) | Healthy when the branch history shows the applied commit is a later commit that contains the promoted one and no commit after ours changed the environment's files (`lastAppliedRevision <rev> contains <commit>`). Otherwise healthy when the Kustomization's Deployments (in `status.inventory` or `spec.healthChecks`) run the version images and are rolled out; the message says `(not <commit>, but the Kustomization's Deployments run the Bundle images)`. Otherwise Wait, then `onHealthFailure` at `health.timeout`. A Kustomization with `spec.kubeConfig` (another cluster) has no Deployments kardinal can read, so it waits |
| `Ready=Unknown` while Flux reconciles again the commit it applied (generation observed, `lastAttemptedRevision` equals `lastAppliedRevision`) | Flux sets this at the start of every reconcile, including its interval and a `reconcile.fluxcd.io/requestedAt` request. The result is the result of the Kustomization's Deployments (in `status.inventory` or `spec.healthChecks`) that run a version image repository, with the revision rules above: Healthy keeps a running bake going, and one of them that lost its replicas or stalled in this promotion's rollout is a health failure (a `ProgressDeadlineExceeded` from an earlier rollout waits, see the stall row below). A Deployment that runs none of the version's repositories (a cache, say) and is not healthy makes the check wait, never fail; Flux's own result decides once the reconcile ends. Without Deployments that run a version repository it waits |
| `Ready=Unknown` while Flux applies another commit, or generation not observed yet | Wait |
| `spec.suspend: true` and the promoted commit not applied | Wait; the message starts with `Kustomization <namespace>/<name> is suspended; Flux applies nothing until it is resumed`. `onHealthFailure` at `health.timeout`. A suspended Kustomization that already applied the commit is Healthy |
| `approval: pr-review` and the merge commit is not known yet | Wait, then `onHealthFailure` at `health.timeout` (see below) |
| `Ready=False` because Flux gave up on the promoted commit: its resources stalled (`HealthCheckFailed`, "failed early due to stalled resources", for example a Deployment past its `progressDeadlineSeconds`) | **Failed at once**: `onHealthFailure` applies without waiting for the timeout. Flux fails a Deployment on any `ProgressDeadlineExceeded`, also one from an earlier rollout that Kubernetes keeps after a rollback (see [resource](#adapter-resource-default)), and checks again only at its next reconcile. So when every resource Flux lists is a Deployment in this cluster whose `ProgressDeadlineExceeded` is from an earlier rollout, decided as for the resource adapter (a stall of the current ReplicaSet counts at once only for a `type: image` version whose images the Deployment runs), or that is no longer past its deadline, the check waits: `... (lastAttemptedRevision=<commit>), but no Deployment Flux lists is past a progress deadline of this promotion's rollout: <each Deployment>; waiting for Flux to check again` |
| The same stall on another commit (a sibling environment pushed to the same branch before Flux fetched ours) when a Deployment that runs the version images in its pod template is itself past a progress deadline of this rollout | **Failed at once**; the message says `(lastAttemptedRevision=<other>, not <commit>, but Deployment <namespace>/<name>, which runs the Bundle images, stalled)`. When every resource Flux lists is a Deployment whose `ProgressDeadlineExceeded` is from an earlier rollout, or that is no longer past its deadline, the check waits, as on the promoted commit: `... (lastAttemptedRevision=<other>, not <commit>), but no Deployment Flux lists is past a progress deadline of this promotion's rollout: <each Deployment>; waiting for Flux to check again` |
| `Ready=False` on another commit (`lastAttemptedRevision` is a git commit other than the promoted one) while no Deployment of the Kustomization runs the version images, for example a fix-forward after a failed release before Flux fetched the fix | Wait, not a health failure: Flux has not applied the promoted change, so a `RollbackPolicy` does not roll the fix back. The message says `(lastAttemptedRevision=<other>, not <commit>: Flux has not applied the promoted change, and no Deployment of the Kustomization runs the Bundle images)`. `onHealthFailure` at `health.timeout`. A Kustomization with `spec.kubeConfig` (another cluster) has no Deployments kardinal can read, so it waits too. A later commit of another environment that includes the promoted change and fails before any Deployment runs the version images waits too, until `health.timeout` |
| `Ready=False` otherwise: on the promoted commit (build or apply failed, a health check timed out), on another commit while a Deployment runs the version images (for example a cache Deployment stalled while ours is rolled out, or Flux applied the change to only some of the Deployments), or with a `lastAttemptedRevision` that is empty or not a git commit | Unhealthy (counts as a health failure) |
| Not found | Unhealthy (counts as a health failure) |

**The promoted commit must be known.** For a direct push it is the pushed commit. For a PR (`approval: pr-review`) it is the merge commit: the SCM webhook records it with the merge for GitHub, Forgejo and Gitea, and for GitLab merges that are not fast-forward, and otherwise the controller asks the SCM provider for it after the merge (see [When the merge commit is not known yet](#when-the-merge-commit-is-not-known-yet)). Until it is known, the check waits with `merge commit of the PR not known yet`, because a Kustomization that is `Ready=True` on the **previous** commit would otherwise pass: there is no image check to fall back on, unlike `argocd` and `resource`. If the commit never becomes known, `health.timeout` applies `onHealthFailure` with that reason. That happens when:

- the provider does not return one (for example an Azure DevOps PR without `lastMergeCommit`, or a Bitbucket PR whose `merge_commit` is empty);
- the lookup fails with an error a retry cannot fix (401, 403 that is not a rate limit, 404, 410);
- the lookup keeps failing for 5 minutes after the merge, after which the controller stops asking.

In each case the PRStatus has `status.mergeCommitUnavailable: true`.

For those providers, use `argocd` or `resource`.

## Adapter: argoRollouts

Watches an Argo Rollouts Rollout after promotion until it has rolled out the promoted revision.

```yaml
health:
  type: argoRollouts
  argoRollouts:
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
  timeout: 30m
```

**Healthy when:** the Rollout's pod template (or the Deployment its `spec.workloadRef` names) runs the version images, Argo Rollouts observed that spec (`status.observedGeneration`), `status.phase` = `Healthy`, and the stable ReplicaSet is the current pod template (`status.stableRS` = `status.currentPodHash`).

This adapter is used for `health.type: argoRollouts` and when `delivery.delegate: argoRollouts` is set on the environment. After kardinal-promoter writes the new image tag to Git and the GitOps tool syncs, Argo Rollouts detects the image change and executes the canary or blue-green strategy. The adapter watches the Rollout until it completes.

The check waits until the Rollout's pod template runs the version images (`Rollout <ns>/<name> not updated yet: ...`) and Argo Rollouts observed that spec (`waiting for the Argo Rollouts controller to observe generation ...`). Until then the phase still describes the previous release, so neither a `Healthy` nor a `Degraded` phase from it counts. This needs a version image the Rollout runs. A config-only version has no images, and a template that runs none of the version's repositories (kustomize `newName`) cannot be compared either; before the GitOps tool applies the change, Argo Rollouts has already observed the old spec. So for them a `Healthy` phase counts only when the Rollout's `Healthy` condition became `True` at or after the promoted change can have reached git (its `lastTransitionTime`; Argo Rollouts sets it `False` when it starts rolling out a new revision): when the promotion PR was opened, for a step that opens one (kardinal does not record when the SCM merged it, which is later), or when `git-push` started, for a direct push. A Rollout that turned Healthy between the merge and the first health check still counts. An earlier one waits: `Rollout <ns>/<name>: Rollout phase: Healthy is for an earlier release: its Healthy condition's lastTransitionTime ... is before the promoted change reached git (...); waiting for Argo Rollouts to roll out the change`. A Rollout that another release turned Healthy while the PR was open also counts, as its images cannot tell. A config change that does not change the Rollout's pod template (a ConfigMap the pods do not reference by hash, say) starts no rollout, so the step waits until `health.timeout`; use `argocd` or `flux`, which check the promoted commit, for such changes. A promotion that changed nothing in git has no health-check start, and the phase decides. Then:

| Rollout phase | Adapter behavior |
|---|---|
| `Progressing` | Wait (canary in progress) |
| `Paused` | Wait (a pause step, or paused by hand) |
| `Healthy` | Healthy (canary completed successfully) |
| `Degraded` | Unhealthy (canary aborted or analysis failed, Argo Rollouts serves the stable revision; counts as a health failure). Not final: Argo Rollouts can roll forward again, so without a `bake` window the step fails at `health.timeout`. |

## Adapter: flagger

Watches a Flagger Canary until Flagger has analyzed and promoted the promoted revision. Used for `health.type: flagger` and for `delivery.delegate: flagger`.

```yaml
health:
  type: flagger
  flagger:
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
  timeout: 30m
```

**Healthy when:** `status.phase` = `Succeeded` and the primary Deployment (`<target>-primary`, to which Flagger copies a revision it promotes) runs the version images and is Available.

Flagger keeps the phase of its last analysis until an analysis tick notices that the target changed, so right after the GitOps tool applies a change the phase describes the previous release. The adapter therefore checks the revision first:

- Until the Canary's target Deployment (`spec.targetRef`) runs the version images, the check waits (`Canary <ns>/<name>: target Deployment <ns>/<name> not updated yet: ...`).
- `Succeeded` with a primary on other images is from an earlier release: Wait.
- `Failed` counts only when Flagger set it after the target ran the version images; an earlier `Failed` is from an earlier release: Wait. The previous release's analysis can fail after this health check started but before the GitOps tool applied the version, so the start of the health check is not enough. The first check that finds the target on the version images records the time in the PromotionStep's `status.targetUpdatedAt`; that check, and every later one, waits on a `Failed` set at or before it (in the same second counts as before). Flagger notices a new target before it rolls back, so a `Failed` set later is about the version.
- A `Failed` Canary whose primary runs the version images (the version is the revision Flagger last promoted) is checked like `Succeeded`.
- When the images cannot be compared (a version without images, a Canary without `spec.targetRef`, a target that is not a Deployment, or a target that runs none of the version repositories), `Succeeded` and `Failed` count only when Flagger set them at or after the start of this health check. The same holds for `Succeeded` when Flagger has not created the primary Deployment yet.

When Flagger set the phase is the `lastUpdateTime` of the Canary's `Promoted` condition, whose reason is the phase. `status.lastTransitionTime` is not used for this when that condition is there: Flagger rewrites it at every analysis tick of a `Failed` Canary.

`status.targetUpdatedAt` is when kardinal first saw the target updated, not when the GitOps tool updated it. Checks run every 10 seconds, and an analysis takes at least one Flagger interval, so kardinal sees the update first. If it does not (the controller was down from before the GitOps tool applied the version until after Flagger failed it), the `Failed` looks like the previous release's: the step waits and fails at `health.timeout` instead of at once.

| Canary phase | Adapter behavior |
|---|---|
| `Initializing`, `Initialized`, `Waiting` | Wait |
| `Progressing`, `WaitingPromotion`, `Promoting`, `Finalising` | Wait |
| `Succeeded` (this release) | Healthy |
| `Failed` (this release) | **Failed at once**: Flagger rolled the canary back, so `onHealthFailure` applies without waiting for the timeout |

The step message carries the message of the Canary's `Promoted` condition, for example why Flagger rolled it back.

## Remote Clusters

Run one kardinal controller, next to the Argo CD or Flux hub if you have one. Declare one
environment per cluster or region (for example `prod-eu` and `prod-us`), and use `wave` or
`dependsOn` to promote them in parallel ([examples/multi-cluster-fleet](https://github.com/pnz1990/kardinal-promoter/tree/main/examples/multi-cluster-fleet)).
kardinal writes to git, and there are two ways to verify a workload in another cluster.

**1. Read the cluster directly with a kubeconfig Secret** (`health.kubeconfigSecretRef`). Every
health type then reads its object (Deployment, Application, Kustomization, Rollout, Canary) in the
cluster the kubeconfig selects, instead of the controller's:

```yaml
health:
  type: resource
  resource:
    name: my-app
    namespace: my-app
  kubeconfigSecretRef:
    name: prod-eu-kubeconfig    # a Secret in the Pipeline's namespace
    key: kubeconfig             # default "kubeconfig"
```

- The Secret must be in the Pipeline's namespace (a Pipeline cannot use another namespace's
  credentials) and carry the label `kardinal.io/referenceable: "true"`; without the label the step
  waits with `ClusterUnreachable: SecretNotReferenceable: ...` and the kubeconfig is not used. The
  label is the Secret owner's consent to have it used for another cluster; it is the same rule as
  for the Secrets of MetricChecks, NotificationHooks and Subscriptions. The
  controller reads it at every check, so a rotated Secret is used from the next check.
- Only inline credentials work: a bearer token (`token`), a client certificate and key
  (`client-certificate-data`, `client-key-data`), or `username` and `password`, and the
  `server` must be an `https` URL (an `http` server fails the step: anyone on the path could forge
  its answers). A kubeconfig with `exec`, `auth-provider`, `tokenFile`, a file path
  (`client-certificate`, `client-key`, `certificate-authority`), `proxy-url` or
  `insecure-skip-tls-verify` is refused and the step fails with `kubeconfig not allowed: ... is not
  supported`: these would run a command or read a file inside the controller, or send the
  credentials to a server that is not verified. For EKS or GKE, use a ServiceAccount token in the remote cluster
  (a `kubernetes.io/service-account-token` Secret there, or a token an external tool refreshes into
  the kubeconfig Secret), bound to a role that can `get` and `list` the checked objects, and
  `list` pods in their namespace (the `resource` check reads the new ReplicaSet's pods to name why
  they do not start; without it the message just omits the pod's reason).
- Only the kubeconfig's `current-context` is used. The API server address goes through the
  controller's egress guard (no loopback, link-local or cloud metadata addresses) and is dialled
  directly, not through `HTTP(S)_PROXY`. Each request has a 10 second timeout.
- A cluster that cannot be reached, or a Secret that does not exist yet, is not an unhealthy
  workload: the step shows `waiting for <adapter>: ClusterUnreachable: <reason>`, counts no health
  failure (`status.consecutiveHealthFailures` does not move), and keeps checking every 10 seconds
  until `health.timeout`. The reason is a class (`timed out`, `connection refused or unreachable`,
  `host not found`, `TLS handshake failed`, `unauthorized (HTTP 401)`, `forbidden (HTTP 403)`,
  `destination address is not allowed`, ...); the full error, which can quote the remote server,
  is in the controller log only. During a `bake` an unreachable check stops the window like a
  waiting result: the time the cluster could not be read is not healthy time, and `health.timeout`
  bounds the wait for the next healthy check again.
- Remote health is polled, not watched, and the Bundle's Graph has no health ref node for the
  environment (kro reads only the cluster it runs in; see the
  [Graph capability ledger](design/16-graph-capability-ledger.md#g8-logic-still-outside-the-graph)).

**2. Read the GitOps hub's object.** Run kardinal next to the GitOps hub that manages the cluster:

- **Argo CD hub:** use `type: argocd`. The Applications for every cluster live in the hub, and their status (health, sync and synced revision) covers the workload in the destination cluster.

    ```yaml
    health:
      type: argocd
      argocd:
        name: my-app-prod-eu        # an Application in the hub whose destination is the spoke
    ```

- **Flux hub:** use `type: flux` on a Kustomization in the hub that sets `spec.kubeConfig.secretRef`. Flux applies that Kustomization to the remote cluster, and the flux adapter reads the Kustomization's status in the hub. Set `spec.wait: true` (or `spec.healthChecks`) on the Kustomization so its `Ready` condition covers the remote workloads, not only the apply.

    ```yaml
    health:
      type: flux
      flux:
        name: my-app-prod-eu        # a Kustomization in the hub with spec.kubeConfig.secretRef
        namespace: flux-system
    ```

  With a hub, kardinal sees only what the hub reports: while a managed spoke is unreachable, the hub's status is what Argo CD or Flux last recorded. A cluster that runs its own Argo CD or Flux and is not managed from the hub needs `kubeconfigSecretRef`.

## Timings and failures

| Setting | Value | Effect |
|---|---|---|
| `health.timeout` | default `10m` | Maximum time from entering HealthChecking to the **first** healthy check, and from the moment a `bake` window stops to the next healthy check. When it expires it counts as a health failure (`status.consecutiveHealthFailures`) and applies `onHealthFailure`, with the message `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: ...`. It does not apply while a bake window runs, so a bake longer than the timeout completes. |
| Check interval | 10s | A step is checked at most once every 10s, however often it is reconciled. |
| `bake.minutes` | — | The environment must stay healthy for this long, contiguously, after the first healthy check. A Waiting check stops the window without an alarm (a canary paused at a step, for example). An Unhealthy check is an alarm: with `policy: reset-on-alarm` (default) it stops the window and increments `status.bakeResets`; with `fail-on-alarm` it applies `onHealthFailure`. A stopped window starts again at the next healthy check, and `health.timeout` bounds the wait for it, so a release that stays unhealthy fails at the timeout under either policy. A release that keeps flapping between healthy and unhealthy keeps restarting the window under `reset-on-alarm` (`status.bakeResets` counts the restarts) until a deadline: one full window must complete by `status.bakeFirstStartedAt` (the first window's start, never reset) + `bake.maxDuration` (default `bake.minutes` + `health.timeout`). A window that stops at or after the deadline, or a stopped window not healthy again by then, applies `onHealthFailure` under either policy with `bake: no <minutes>m contiguous healthy window within <max duration> of the first healthy check (bake.minutes + health.timeout; resets=<n>); last result: ...` (`bake.maxDuration` when set). That includes a Waiting check: a canary paused at a step, or paused by hand, after the deadline fails the step, so set `bake.maxDuration` to cover planned pauses. A window running at the deadline may still complete. Use `fail-on-alarm` to fail on the first unhealthy check. |

Each health check has one of four results:

- **Healthy** — Verified (or the bake window starts or advances).
- **Waiting** — the promoted revision is still rolling out or syncing. It does not count as a failure.
- **Unhealthy** — for example Degraded or `Ready=False` once the promoted change is deployed, not found, or replicas unavailable after the rollout finished. Each check increments `status.consecutiveHealthFailures`, which a `RollbackPolicy` you create reads (see [Rollback](rollback.md)).
- **Failed** — Deployment `ProgressDeadlineExceeded` (from this promotion's rollout, see [resource](#adapter-resource-default)), Flagger canary `Failed` or a Flux Kustomization whose resources stalled, on the promoted revision and not only on deadlines from an earlier rollout (for Flux, also on another commit when the stalled Deployment runs the version images). `onHealthFailure` (`none` → Failed, `abort` → AbortedByAlarm, `rollback` → RollingBack, or AbortedByAlarm when the version is itself a rollback or there is nothing safe to roll back to) applies at once.

Reaching `health.timeout` without a Healthy result is treated like a Failed result: it is counted and applies `onHealthFailure`. A new image that crash-loops is **Waiting**, not Unhealthy: Kubernetes reports the rollout as still progressing (`Progressing=True`, reason `ReplicaSetUpdated`) until the Deployment's `progressDeadlineSeconds` (default 600s) passes. Set `progressDeadlineSeconds` below `health.timeout` to fail such a rollout sooner; otherwise the timeout fails it.

An error reading the target from the API server (not a "not found") is retried at the next interval and does not count as a failure.

## Health Check Defaults

When sub-fields are omitted, the controller applies these defaults:

| Default | Value |
|---|---|
| type | `resource` (or `delivery.delegate` when set) |
| resource.kind | Deployment (the only supported kind) |
| resource.name | Pipeline.metadata.name |
| resource.namespace | Environment name |
| resource.condition | Available |
| argocd.name / argocd.namespace | `<pipeline>-<environment>` / `argocd` |
| flux.name / flux.namespace | `<pipeline>-<environment>` / `flux-system` |
| argoRollouts.name / argoRollouts.namespace | Pipeline.metadata.name / environment name |
| flagger.name / flagger.namespace | Pipeline.metadata.name / environment name |
| timeout | 10m |


## Sync on promotion

Argo CD and Flux poll git: Argo CD every 3 minutes plus up to 1 minute of jitter by default (`timeout.reconciliation`), Flux at each GitRepository's `spec.interval`. Without a webhook from your git server to the tool, a promoted commit would wait for that poll in every environment. So when a step's commit is in git (pushed straight to the branch, or its PR merged), the step asks the object its health check names to sync now, once, as it starts checking health:

| `health.type` | What the step sets |
|---|---|
| `argocd` | `argocd.argoproj.io/refresh: normal` on the Application (`health.argocd`). Argo CD refreshes it at once, removes the annotation, and an Application with automated sync then syncs the new commit |
| `flux` | `reconcile.fluxcd.io/requestedAt: <time> <namespace>/<step>` on the Kustomization's source GitRepository (`spec.sourceRef`, when it is a GitRepository) and on the Kustomization (`health.flux`), as `flux reconcile kustomization --with-source` does. The source fetches the commit, then the Kustomization applies it. `<time>` is `status.refreshRequestedAt`; the step's name makes the value unique, so two steps that ask in the same second (a fan-out whose environments share one GitRepository) each get a reconcile |

The step records the request on its own status: `status.refreshRequestedAt`, `status.refreshRevision` (the commit) and `status.refreshMessage` (which objects, or why the request failed), and emits a `GitOpsRefreshRequested` Event. It records the request before it sends it, so a controller restart never asks twice; a restart between the two sends nothing, and that step waits for the poll. A step with no changes waits for the branch head it cloned, which another writer pushed: it asks after its first health check that does not pass (the tool has not applied that head yet), and not at all when the tool already applied it. A step whose PR merge commit is still being looked up (it asks once the commit is known, or once the PRStatus gives up on it) and `update.strategy: argocd`, which writes no commit, do not ask.

**A failed request is not a failure, and is not retried.** When the patch is refused (no RBAC, the object is missing) the step writes `refresh request failed (...); waiting for Argo CD to poll git` (or Flux) to `status.refreshMessage`, emits a `GitOpsRefreshFailed` Warning Event, and checks health as before: the tool's own poll syncs the commit, within `health.timeout`. The step asks once: an error reading the Kustomization for its source, even a passing one such as an API timeout, uses up that request too, and the step waits for Flux's poll. The message names the object and the class of the error (`annotate Application argocd/my-app-prod: forbidden (HTTP 403)`), never the API server's answer or address; the full error is in the controller log.

**What it does not do.** It does not sync an Application without automated sync, so `health.timeout` still applies to one you sync by hand. It does not refresh a Flux source that is an OCIRepository or a Bucket, only the Kustomization, nor a HelmRelease (`health.flux` names a Kustomization). Other health types (`resource`, `argoRollouts`, `flagger`) name no GitOps object and ask nothing. With [`health.kubeconfigSecretRef`](#remote-clusters) the request goes to the remote cluster, with the kubeconfig's identity, which needs `patch` there; the chart's admission policy (below) is not installed there.

**RBAC and the admission policy.** RBAC cannot limit a `patch` to one annotation, so while `health.refresh.enabled` is on (the default) the chart grants the controller `patch` on `argoproj.io` Applications, Flux Kustomizations and `source.toolkit.fluxcd.io` GitRepositories, and ships the ValidatingAdmissionPolicy `<fullname>-gitops-writes`, bound cluster-wide, which confines it. For a request from the controller's ServiceAccount (and the users in `admission.controllerUsernames`) that updates an Application, a Kustomization, a HelmRelease or a GitRepository, it allows only a change of `argocd.argoproj.io/refresh` (Applications) or `reconcile.fluxcd.io/requestedAt` (the Flux kinds): every other field must be unchanged (the spec, the status, an Application's `operation`, the labels, every other annotation, the finalizers, the owner references), except what the API server maintains itself (`resourceVersion`, `managedFields`, `generation`). Anything else is denied with `the kardinal controller may change only the <annotation> annotation of <kind> <namespace>/<name> unless it may update it`, unless the controller is also allowed to `update` that object. `rbac.argocdApplicationsWrite=true`, which the [argocd update strategy](argocd-native-promotion.md#required-rbac) needs, grants `patch` and `update` on every Application, so the strategy changes their spec as before; a Role of your own that grants `update` on some Applications does the same for those only. `health.refresh.enabled: false` drops the `patch` grants on Kustomizations and GitRepositories, and on Applications unless `rbac.argocdApplicationsWrite` is set; the policy stays and matches nothing the controller may write. The policy guards only the cluster the chart is installed in: with `health.kubeconfigSecretRef` the refresh patches the remote cluster's object as the kubeconfig's identity, and nothing there confines that identity's `patch` to the annotation, so grant it `patch` only on the objects its health checks name, or install an equivalent policy in that cluster.

**Turning it off.** It is on by default. `health.refresh: false` on an environment turns it off there; the chart's `health.refresh.enabled: false` (`--health-refresh=false`) turns it off everywhere and drops the grants above.

```yaml
environments:
  - name: prod
    health:
      type: argocd
      refresh: false   # wait for Argo CD's own poll in prod
```

A git webhook from your git server to Argo CD or Flux syncs as fast and needs neither; the request then only repeats it.
