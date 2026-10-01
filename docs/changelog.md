# Changelog

All notable changes to kardinal-promoter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Before you upgrade

Org gates now read `metrics.*` from the MetricChecks of their org policy namespace (see Changed). Check the org gates whose expressions read `metrics.*`:

1. Create the MetricChecks they use in the org policy namespace. `metrics["x"].result == "Pass"` blocks when that namespace has no MetricCheck `x`, even if the Pipeline namespace has one.
2. Review expressions that allow a missing metric: `!("x" in metrics) || metrics["x"].result == "Pass"` now passes when the org policy namespace has no MetricCheck `x`, whatever a team MetricCheck `x` says.
3. Gate instances created before the upgrade have no `kardinal.io/gate-template-namespace` label, so they keep reading the Pipeline namespace until their Bundle's Graph is translated again: a new Bundle's instances have the label, and an in-flight Bundle's get it when its Pipeline's spec changes.

### Changed

- **An org gate reads the org's MetricChecks** — the Graph creates every gate instance in the Pipeline namespace, and the gate read `metrics.*` there, so a team MetricCheck of the same name decided an org gate and the org's own MetricCheck was never read. Each instance is now labelled `kardinal.io/gate-template-namespace` with its template's namespace. A gate from an org policy namespace (`--policy-namespaces`) reads the MetricChecks of that namespace; every other gate reads the Pipeline namespace. A team can edit its instances' labels, so the label counts only when the org namespace holds the template that `kardinal.io/gate-template` names; if the controller cannot read the template, the gate evaluates with no metrics and an expression that reads `metrics.*` blocks. A change to an org MetricCheck re-evaluates its gates in every namespace, and `kardinal policy simulate` resolves metrics the same way. See [Matching](https://pnz1990.github.io/kardinal-promoter/policy-gates/#matching)

### Fixed

- **The UI records who promoted or rolled back** — with TokenReview auth (`ui.auth.tokenReview`, `--ui-tokenreview-auth`), a UI promote or rollback recorded `kardinal-ui` in the Bundle's `kardinal.io/requested-by` annotation, and so in the rollback PR's "Rolled back by" line, although the controller had authenticated the caller. It now records the caller's Kubernetes username as the API server returned it, for example `system:serviceaccount:team-a:deployer`, and the controller's UI promote, rollback, pause and resume log lines carry it as `requestedBy`. With the static UI token or no UI auth the UI still records `kardinal-ui`
- **A gate approved from the UI names the UI** — with the static UI token or no UI auth, a UI gate approval recorded `ui-action` as the override's `createdBy`, so the gate's reason, `kardinal explain`, the UI and the PR evidence gate table read `OVERRIDDEN by ui-action: ...`. It now records `kardinal-ui`, the value the UI writes in `kardinal.io/requested-by`. With TokenReview auth it records the caller's Kubernetes username, as before. Scripts that match `ui-action` need an update
- **A Bundle created from the UI records who created it** — the UI's Create Bundle (`POST /api/v1/ui/bundles`) set no `kardinal.io/requested-by` annotation. It now sets it as a UI promote does: the caller's Kubernetes username with TokenReview auth, `kardinal-ui` otherwise, and the `ui: bundle created` log line carries it as `requestedBy`. `spec.provenance.author` is still the author typed in the dialog
- **Flux health fails at once on a stalled rollout** — when Flux gives up on the promoted commit because its resources stalled (`Ready=False`, `HealthCheckFailed`, "failed early due to stalled resources", for example a Deployment past its `progressDeadlineSeconds`), the step now fails at once and `onHealthFailure` applies, as the resource adapter does, instead of waiting for `health.timeout`. The same holds when Flux stalls on another environment's later commit on the same branch, if a Deployment that runs the Bundle images is itself past its progress deadline. Any other `Ready=False` still fails the step only at `health.timeout`
- **Flux health on a branch shared by several environments** — Flux can apply another environment's later commit before it reports the promoted one, so the step waited and failed at `health.timeout` although its Deployments ran the Bundle. Like the Argo CD adapter, the flux adapter now accepts another applied commit when the Kustomization's Deployments (`status.inventory` and `spec.healthChecks`) run the Bundle images and are rolled out, and says so in the message. Both compare images, not git history, so they do not check that the other commit is later. A Kustomization with `spec.kubeConfig` still waits. The health adapter docs no longer advise one branch per environment, which a Pipeline cannot set
- **A flux bake survives Flux reconciling again** — Flux sets `Ready=Unknown` at the start of every reconcile, also when it only reconciles again the commit it applied (its interval, `flux reconcile`, a receiver). The adapter waited, so a bake that hit that window started over, or never finished when Flux reconciled often. In that window the result now comes from the Kustomization's Deployments that run a Bundle image repository: a healthy one keeps the bake going, and one that lost its replicas or stalled is a health failure. Another Deployment (a cache, say) that is not healthy makes the check wait, never fail. Without Deployments that run a Bundle repository it still waits
- **Flux health messages** — the pass message ends with `lastAppliedRevision=<commit>`. A promotion into a suspended Kustomization waited until `health.timeout` with no hint why; its message now starts with `Kustomization <namespace>/<name> is suspended; Flux applies nothing until it is resumed`. A suspended Kustomization that already applied the commit is still healthy
- **A Deployment rollout that waits for new pods says so** — when the new pods of a rolling update never become available (an image that cannot be pulled), the resource adapter's message read `1 old replicas pending termination`, which points at the old pods. It now says the rollout waits for new pods to become available
- **A merge commit learned after the merge reaches the step outputs** — a webhook can mark a PRStatus merged before the merge commit is known (Forgejo's event has none), and the step copied `status.outputs.mergeCommitSHA` only when it left WaitingForMerge, so it never got it. The health check now copies it once the PRStatus has it. The health check itself already used the right commit
- **A failed MetricCheck status write no longer backs off** — a failed status patch went into the controller's error backoff: eight retries in the first second, each querying Prometheus, then ever longer waits up to 1000 seconds, so the next evaluation could come minutes after the write worked again. The controller now evaluates again and retries the write after 5 seconds the first time, then every `interval`, until a write works. Gates fail closed meanwhile, once the result passes `status.validUntil`
- **A gate's evaluation error starts with the Bundle version** — like every other gate reason, the reason of a CEL evaluation error now starts with `bundle.version=<version>:`
- **Step durations are real** — a step that starts and finishes in one reconcile (every step but wait-for-merge) got the same `startedAt` and `completedAt`, `durationMs` 0, and no `kardinal_step_duration_seconds` sample. Wait-for-merge and the health check were never observed, and the health-check placeholder was observed at about 0s. Every Completed or Failed step now has its real times and `durationMs`, and is observed once, after the status write that records it, also when that write fails and is retried
- **`kardinal_pr_duration_seconds` measures from the PR opening** — it measured from the PRStatus creation, which comes with the Bundle, so it also counted the upstream environments, the gates and the steps before open-pr. It now measures from the open-pr step's completion to the merge, observed once the step's status records the merge; a step deleted meanwhile is not observed
- **The chart's Grafana dashboard works when the Grafana sidecar loads it** — its panels named the datasource `${DS_PROMETHEUS}`, which only Grafana's import dialog fills in, so with `grafanaDashboard.enabled` every panel showed `Datasource ${DS_PROMETHEUS} was not found` and ran no query. The panels now use the dashboard's **Prometheus** datasource selector, which starts at the default Prometheus datasource. Importing the JSON by hand no longer asks for a datasource
- **Docs** — a MetricCheck `interval` of `0` or less means the 1m default, not 10s (the CRD description and API reference said 10s; apply the new CRDs to see it in `kubectl explain`). The monitoring guide lists the `requeue_after` result of `controller_runtime_reconcile_total` and all 11 `controller` label values, and says when the PR and step durations are measured. The Kargo migration guide says MetricCheck supports only Prometheus. The flux-demo README describes every flux health state, and the health adapter and troubleshooting guides cover a suspended Kustomization
- **Error messages name each layer once** — a failed step showed its name twice (`step argocd-set-image: argocd-set-image: patch Application: ...`), and a Bundle that failed Graph build showed `translator.Translate: build: build: ...`. A step error now reads `step <name>: <what failed>` (`step argocd-set-image: patch Application argocd/my-app: ...`), a Graph build error `translator.Translate: build: ...`, and `kardinal create bundle --dry-run` and `kardinal policy simulate` show the builder's `build: ...` error once. A failed clone names the repository URL once (`step git-clone: git clone <url>: authentication required: Unauthorized`, and `step git-clone: config source: ...` for a config commit), and a git error no longer ends with the newline of the git server's response, or with `: ` when the response was empty
- **`RollbackSucceeded` AuditEvents are written** — the action was in the AuditEvent CRD but nothing wrote it. A PromotionStep of a rollback Bundle (from `kardinal rollback`, the UI, a RollbackPolicy or `onHealthFailure: rollback`) that reaches Verified now writes a `RollbackSucceeded` AuditEvent, besides `PromotionSucceeded`, once per step (it is named `<step>-rollback-succeeded`, so a repeated reconcile writes no second one). `kardinal audit summary` does not count it as another promotion or rollback
- **A missing git credential is named** — when `spec.git.secretRef` was not set or named a Secret that does not exist, the controller only logged a warning, and `git-clone` or `git-push` failed with `authentication required` and gave up after 5 retries. The step message now ends with what is missing (`(git Secret <ns>/<name> not found)`, `(git Secret <ns>/<name> has no token key)` or `(spec.git.secretRef is not set, so the push has no credentials)`), the PromotionStep gets a `GitCredentialMissing` condition and one `Warning` Event, and the step retries every 2 minutes until git has a token, so creating the Secret lets it continue. These retries are counted in the new `status.gitCredentialRetries`, not in `status.retryCount`, so other git errors, before or after the Secret is created, still get 5 retries and then fail the step. A Secret the controller cannot read (`(git Secret <ns>/<name> could not be read: <error>)`, reason `SecretUnreadable`) is not fixed by creating it, so that step keeps the limit of 5 retries. Apply the new PromotionStep CRD by hand: Helm does not update CRDs, and the API server drops `status.gitCredentialRetries` until the CRD has it
- **RollbackPolicy `SHOULDROLLBACK` follows the failures** — once the threshold was reached and the rollback was refused, `status.shouldRollback` stayed `true` after the health check passed again, and a later evaluation could still create a rollback Bundle with no failures. It is `false` again below the threshold until a rollback Bundle exists, and `RollbackRefused` turns `False` with reason `BelowThreshold`; it used to stay `True`. A policy whose rollback Bundle was created but not recorded (the controller stopped in between) stays `true` and records it. The CRD now defaults `spec.failureThreshold` to 3, so `kubectl get rollbackpolicy` shows it in `THRESHOLD`, and a policy is no longer polled every 30 seconds: it is evaluated when its spec changes and when a PromotionStep of its Bundle appears or its status changes. Apply the new RollbackPolicy CRD by hand: Helm does not update CRDs
- **A NotificationHook no longer POSTs an event twice** — the reconciler read the hook from the informer cache, so a reconcile that started a few milliseconds after the previous one's status write could miss its record of the attempt and send the same event again, after a failed or a delivered POST. It now reads the hook from the API server, and every qualifying event is delivered once, as docs/notifications.md says
- **`kardinal policy list` shows when each gate was last evaluated** — LAST-EVALUATED read the template, which the controller never evaluates, so it was always `-`. It now shows the newest evaluation of the gate's instances (with `--pipeline`, of that pipeline's instances). Gate instances get a new `kardinal.io/gate-template-namespace` label, so a gate in an org policy namespace or in `spec.policyNamespaces` is matched to its instances in every Pipeline's namespace
- **A blocked gate shows its message** — when a PolicyGate's expression is false, its `status.reason` starts with `spec.message` and the evaluated result follows in parentheses, so the `Ready` condition, `kardinal explain`, the UI, PR evidence and notifications show the message. `kardinal status <pipeline>` shows the message under Blocking Policy Gates, and the Pending step's message is `waiting for gate <name>: <message>`. A gate without a message, an evaluation error and a context error keep their reasons. Scripts that expect `status.reason` to start with `bundle.version=` need an update
- **PolicyGate names over 63 characters are rejected whatever the name** — the name rule let through any name containing `--` or starting with `freeze-`, so a template could have a name too long for the `kardinal.io/gate-template` label of its instances, and every Bundle of a Pipeline it applied to failed at Graph build. Only the gates kardinal creates (gate instances and pause freeze gates) may be longer now. Kardinal sets the new `spec.generated` field on them and never uses a gate with it as a template; the controller sets it on the ones created before the upgrade. Apply the new PolicyGate CRD by hand: Helm does not update CRDs
- **Mixed Bundles deploy their config commit** — a `type: mixed` Bundle ran the image steps only, so its `configRef` change was never merged. It now runs `config-merge` and then the image update, in one commit per environment
- **Rolling back a mixed Bundle restores its config commit** — rollback treated a `type: mixed` Bundle like an image Bundle, so it restored the images and left the mixed Bundle's config commit deployed. The rollback now also carries the target's config commit or, if the target has none, the newest earlier Verified one, and a mixed Bundle is a source of both images and config commits. `--to` a mixed Bundle works when a config Bundle is deployed. When a mixed Bundle is deployed, `--to` an image Bundle is refused if the mixed Bundle changed the config commit, and `--to` a config Bundle if it changed an image; the error names what the target cannot restore. A rollback that would change nothing the environment runs is refused (with `--to`) or skipped (without), also when what it would deploy comes from Bundles before the deployed one, for example `--to` a mixed Bundle whose config commit is still deployed under an image Bundle (see [Rollback](rollback.md#images-the-target-does-not-name))
- **A mixed rollback PR names its config commit** — the title and the FROM and TO lines of a rollback PR named only the images of a mixed Bundle, although the rollback also deploys its config commit. They now name both: `[kardinal] Rollback prod to <bundle> (restores 1.28.0 with config 0123abc)`. Config Bundles already showed `config <commit>`
- **A superseded Bundle's PR closes at once** — the controller closed it at the step's next merge poll, up to 30 seconds later, and the PR could be merged in that window, changing the environment with no PromotionStep tracking it
- **Rollback PRs name what they replace** — the title names the restored version (`[kardinal] Rollback prod to <bundle> (restores 1.28.0)`), the body's FROM is the Bundle being replaced and TO the one restored, and a "Rolled back by" line names who ran it. The rollback Bundle keeps the restored build's `spec.provenance`, so a gate on `bundle.provenance.author` sees the build's author; who ran the rollback is in the `kardinal.io/requested-by` annotation, as for `kardinal promote`
- **Webhook event types** — Forgejo, Gitea, GitHub and GitLab webhooks take the event type from the event header (GitLab: `object_kind`), so push, comment and review events are no longer counted as pull request events
- **No false "SCM credentials rotated" log** — the token watcher's first read logs "loaded"; "rotated" means the token changed, as the rotation guide expects
- **Forgejo/Gitea token check** — a 403 from `/user` with the documented scopes is logged at info as "token scopes not checked" with the configured provider, not as a network error at debug level
- **PRStatus placeholders are not polled** — a PRStatus without a PR number was requeued every 30 seconds for every Bundle; it now waits for the PromotionStep to set the PR
- **The UI's Create Bundle checks the Bundle like the Bundle API** — `POST /api/v1/ui/bundles` created a Bundle for a Pipeline that does not exist, and answered every other error with 500 `failed to create bundle`. It now answers 404 for a missing Pipeline, 400 with the reason for an invalid pipeline name or a Bundle the API server refuses, and 403 when the caller may not create it, and the dialog shows the reason
- **Bundle API right after a new Pipeline** — `POST /api/v1/bundles` read the Pipeline from the controller's cache and answered 404 for a Pipeline created moments before; it now reads the API server before answering 404
- **UI bundle comparison opens from Compare** — a shift-click opened the comparison at once, so the Compare and clear buttons sat behind the dialog where nothing could reach them. A shift-click now picks the second Bundle, Compare opens the comparison, and closing it keeps the second Bundle. A shift-click on the shown Bundle, or a `bundle=` link naming it, no longer compares the Bundle with itself
- **UI keeps the pipeline list while a poll fails** — the sidebar replaced the list and its filter with the error until a poll worked; it now shows the error above the list. The stale-data indicator turns red after 30 seconds of failed polls, as documented, instead of staying amber, and the fleet bar badges read "1 blocked pipeline" (not "pipelines") to a screen reader
- **Deleting a Bundle closes its open PR** — deleting a Bundle, a PromotionStep or their namespace deleted the steps and left their promotion PRs open and mergeable, and merging one later changed the environment with no PromotionStep tracking it. A step that opens a PR (a `pr-review` environment) now holds the `kardinal.io/close-pr` finalizer while it is `Promoting` or `WaitingForMerge`, from before it opens the PR; an `auto` step never holds it. On delete the controller asks the SCM whether the PR is still open, closes it with a comment if it is (the comment says whether the Bundle, its namespace or the step was deleted), then lets the step go. A PR merged or closed meanwhile is left alone, with no comment, so Bitbucket and Azure DevOps are never asked to decline or abandon a merged PR. A step deleted on its own (`kubectl delete promotionstep`) is created again by kro once it is gone: its PR is closed with a comment, and the new step opens a new one. A step deleted with its Graph while the Bundle is still `Promoting` keeps its PR open, because the controller recreates the Graph and the new step reuses that PR; if the Bundle is deleted or stops `Promoting` before the new step exists, that PR stays open and must be closed by hand. If the SCM keeps failing it gives up after about 5 minutes with the error log `gave up closing the PR of a deleted PromotionStep` and a `ClosePRFailed` Warning Event; in a namespace being deleted no Event can be recorded, so the log is the only record. Delete your Bundles and wait for their PromotionSteps to go before `helm uninstall`: without the controller the finalizer stays (see [Uninstall](installation.md#uninstall)). A controller from v0.9.0-rc.1 or earlier never removes the finalizer, so after a downgrade remove it by hand (see [Downgrading](installation.md#downgrading))
- **Deleting a namespace with Bundles finishes** — the namespace deletion could delete the applier RoleBinding before kro had torn the Graph down; kro deletes as the Graph ServiceAccount, so every delete was then forbidden, kro kept its finalizer, and the namespace stayed Terminating for good. The controller now removes kro's finalizer from its own Graphs once their namespace is Terminating and the applier RoleBinding is gone, and the namespace deletion removes the rest. A Graph whose teardown was already stuck before the namespace deletion started (its applier RoleBinding deleted first) is found too: a Graph being deleted is checked again every 30 seconds while its namespace is not Terminating. The controller needs `get` on namespaces, which this release adds to the chart (limited to `controller.watchNamespace` in namespace mode)
- **Reader RoleBindings go with the last Graph** — the reader RoleBindings that let a namespace's Graph ServiceAccount read health objects in `argocd`, `flux-system` or other allowed namespaces were pruned only when a Bundle was translated, so they stayed for good after a namespace's last Bundle, or the namespace itself, was deleted. The controller now prunes them when a Graph is deleted, and in cluster mode a sweep at startup and every 10 minutes deletes the ones whose namespace is gone or holds no Graph reading through them, including those an older version left. The sweep touches only RoleBindings labeled `app.kubernetes.io/managed-by=kardinal-promoter`; the controller needs `list` on RoleBindings, which this release adds to the chart. In namespace mode the prune looks in the watch namespace only, so it logs no forbidden reads of `argocd` or `flux-system`, and a binding the controller cannot read stays recorded and is tried again
- **No Graph errors for the Bundles of a namespace being deleted** — while a namespace with Bundles was Terminating, the namespace deletion removed their Graphs and the controller kept recreating them; each try failed creating the Graph ServiceAccount (`create serviceaccount ... because it is being terminated`) and added a `GraphSyncFailed` or `TranslationError` Warning and a `GraphSynced=False` condition until the Bundle went. The controller no longer translates a Bundle whose namespace is being deleted, and no longer marks one `PipelineNotFound` with a Warning when the namespace deletion removed its Pipeline first. It reads the namespace only before it translates or when the Pipeline is missing, with the `get` on namespaces this release adds to the chart (limited to `controller.watchNamespace` in namespace mode). A paused Pipeline in such a namespace no longer logs a `pause: create freeze gate` error on every retry, and a translation the namespace deletion interrupts no longer logs a `graph identity: reader role not bound` warning.

---

## [v0.9.0-rc.1] — 2026-09-30

**Release candidate: kardinal runs on upstream kro Graph. Breaking changes: read [Upgrading from v0.8.1](https://pnz1990.github.io/kardinal-promoter/installation/#upgrading-from-v081) first.**

Pin the version: without `--version 0.9.0-rc.1`, `helm install` and `helm upgrade` pick v0.8.1. The CLI is on the release page (`kardinal-<os>-<arch>`).

### Before you upgrade

Follow the tested steps in [Upgrading from v0.8.1](https://pnz1990.github.io/kardinal-promoter/installation/#upgrading-from-v081). A plain `helm upgrade` does not work. In short:

1. Find and fix the stored objects the new CRDs reject (list below). This is required on Kubernetes older than 1.30, which does not ratchet CRD validation. A PolicyGate name over 63 characters blocks every write on any version, unless kardinal created the gate: gate instances and pause freeze gates can be longer, and the new controller sets `spec.generated` on them.
2. Stop the v0.8.1 controller and its bundled Graph controller (krocodile), and remove the krocodile finalizers from the old `graphs.experimental.kro.run` Graphs.
3. Annotate the `kro-system` namespace with `helm.sh/resource-policy=keep`. The v0.8.1 chart created it, and `helm upgrade` would delete it, with kro in it.
4. Install kro v0.10.0-rc.0 with the `GraphKind` feature gate (`hack/install-kro.sh`).
5. Apply the new CRDs by hand. Helm never updates CRDs, and the v0.8.1 chart shipped none.
6. Upgrade with `--reset-then-reuse-values` (Helm 3.14 or later). `--reuse-values` keeps the v0.8.1 `krocodile` default, which the new chart rejects.
7. Check the controller's UI exposure: with no UI auth mode set, the UI API answers only `kubectl port-forward` clients.

The API server now rejects: `spec.environments[].steps` (non-empty), `promotionTemplate`, `autoRollback`, `update.strategy: argocd` with `approval: pr-review`, a reserved environment name (such as `kind`, `spec` or `bundle`), an environment name that is not a lowercase DNS label of at most 63 characters, a non-empty `spec.policyGates`, a PolicyGate `spec.selector`, and a PolicyGate name over 63 characters on a gate kardinal did not create (it marks its own with `spec.generated`). The fields were ignored or failed only at promotion time; a reserved name clashes with kro Graph node IDs.

### Changed

- **Upstream kro Graph** — kardinal renders one upstream kro `kro.run/v1alpha1` Graph per Bundle, instead of using a forked Graph controller. Pipeline changes update the Graph in place instead of re-running Verified environments. In-flight Bundles are re-translated once after upgrading. What still runs outside the Graph: [Graph Coverage](https://pnz1990.github.io/kardinal-promoter/graph-coverage/)
- **Gates are re-checked before a step starts** — a PromotionStep starts only when every gate it requires is ready, with a result evaluated at or after the step was created. A step that has started is not stopped by a gate that turns false later. Messages: `waiting for gate <name>` and `waiting for gate <name> to be re-evaluated`. `PolicyGate.spec.when` is deprecated and has no effect. Right after the upgrade, a Pending step whose gate results are older than the step waits for the next evaluation; the controller re-evaluates every gate when it starts
- **Stale MetricCheck results fail closed** — MetricCheck has `status.validUntil` (three intervals after the evaluation, at least 30s). After it, gates see `metrics.<name>.result` as `"Stale"`, `.value` as `""` and `.stale` as `true`. Expressions written as `result != "Fail"` pass on a stale result; write `result == "Pass"`. Right after the upgrade every MetricCheck is stale until its first evaluation
- **Pause holds promotions** — `kardinal pause` and the UI stop new steps and hold in-flight ones at the next safe point; resume continues them within a minute. The Pipeline reconciler keeps the `freeze-<pipeline>` gate in step with `spec.paused` and reports it in a `Paused` condition. A Pipeline paused from the old UI, or with `spec.paused: true` set directly, had no freeze gate and was not actually paused; after upgrading it stops promoting. Run `kardinal resume <pipeline>` for any that should keep running (`kubectl get pipelines -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,PAUSED:.spec.paused`)
- **Rollback restores the previous verified Bundle** — `kardinal rollback`, the UI, `onHealthFailure: rollback` and RollbackPolicy share one implementation. The target is the most recent other Bundle Verified in the environment; the rollback restores every image the deployed Bundle changed, using the environment's history for images the target does not name, and refuses when one cannot be found. `--to` must name a Bundle Verified in the environment and of the same type. A rolled-back image is never promoted again, the automatic paths never roll back a rollback, and a rollback also goes through the environments upstream of the target. Rollback PR titles say "(restores <bundle>)" and carry `kardinal/rollback`. A refused auto-rollback shows as `RollbackRefused=True` on the RollbackPolicy, a Warning Event and a `Refused` column. See [Rollback](https://pnz1990.github.io/kardinal-promoter/rollback/)
- **`kardinal promote`** copies the artifacts of the newest Bundle Verified in every upstream environment, and refuses instead of creating a Bundle without images
- **A Superseded Bundle stops promoting** — its Graph creates no new step, its gates keep their last status, and a step that never started is failed without an AuditEvent. A Graph built before the upgrade can still create one step at the moment its Bundle is superseded; that step fails the same way. Gates of Verified Bundles are no longer evaluated either
- **UI API is local-only without auth** — with no UI auth mode set, `/api/` answers only local clients (`kubectl port-forward`). Through an Ingress, NodePort or LoadBalancer it returns `403`: set `ui.auth.tokenReview=true` or `ui.auth.tokenSecretRef.name`. Behind a service-mesh sidecar, set an auth mode too
- **Egress guard** — NotificationHook webhooks and MetricCheck Prometheus URLs can no longer reach loopback, link-local (cloud metadata), unspecified or multicast addresses. The check runs on the resolved address at connect time, and, when `HTTP(S)_PROXY` is set, on the target before it goes to the proxy, so the controller pod then needs DNS for the target hosts. Private addresses and cluster Services still work
- **Controller Events** use `events.k8s.io/v1`, with an action and a note of at most 1024 bytes. Custom RBAC must allow create and patch on `events.k8s.io` events. Repeated events merge within about 6 minutes, and the UI's event count refreshes every 30 minutes
- **Secret RBAC is `get` only** — the controller never lists or watches Secrets; the release-namespace Role gets `get` on the SCM token Secret by name
- **Flux health on `pr-review` steps** waits for the PR's merge commit instead of passing on a Kustomization Ready on the previous commit; the GitHub and GitLab webhooks record the merge commit. **Argo CD health** is not ready while a sync is running or has failed. Flux waits for `observedGeneration`; Flagger messages include Flagger's reason
- **PRStatus** — a PR closed without merging can be reopened within 5 minutes and the promotion continues. After that kardinal comments once, stops polling and fails the step (`closedAt`, `closedFinal`)
- **Bundle lifecycle** — a Bundle is `Verified` when every environment it targets is Verified and `Failed` when a step fails or kro rejects the Graph. A Pipeline, intent or PolicyGate that cannot be built into a Graph fails the Bundle with `InvalidSpec` instead of retrying forever, and so does a Bundle without the artifacts its type needs (including a `mixed` Bundle created with kubectl without `configRef.commitSHA`). A Bundle that failed before its Graph was built retries after its Pipeline changes. `intent.targetEnvironment` Bundles finish. The Graph's `Accepted` and `Ready` conditions show on the Bundle; `GraphReady` turns True when a Verified Bundle's Graph is ready. `maxConcurrentPromotions` counts only promoting Bundles, read from the API server. Bundles created in the same second are ordered by `kardinal.io/created-at`
- **Pipeline status** — `Ready` is True once the spec is valid, and False with the reason otherwise (an unknown `dependsOn`, a cycle, a `git.secretRef` in another namespace, or `NotImplemented` for `layout: branch`, `shard`, `health.cluster`, a `health.resource.kind` other than `Deployment` or two or more `regions`). The API server refuses two environments with the same name. `status.phase` is `Promoting` while a Bundle is in flight or held by a gate. `historyLimit` defaults to 50
- **`kardinal explain`, `status` and `get pipelines`** describe the current Bundle (the newest that is not Superseded, as in the UI). `explain` and `status <pipeline>` add a BUNDLE column and show the Bundle deployed in an environment the current Bundle has not reached: scripts that parse columns by position need an update. Gate states match the UI: **Block** only while the gate holds the Bundle, otherwise **Waiting**, **Pending** or **Superseded**. The UI's blocked banner, **Show blocked** and the gate panel count only gates that hold the Bundle
- **`kardinal override`** works on real gate instance names (it patched nothing for any name over 63 characters) and, for a template name, patches that gate's instances in the pipeline's in-progress Bundles. `Bundle.status.metrics.operatorInterventions` counts overrides
- **`kardinal create bundle`** applies the same checks as `POST /api/v1/bundles` and adds `--config-commit`, `--config-repo`, `--commit`, `--author` and `--ci-run-url`. `--image repo@sha256:...` records a digest. `provenance.ciRunURL` must be an absolute `http(s)` URL; the PR body, UI and bundle comparison show `—` instead of linking anything else
- **Subscription digest label** `kardinal.io/source-digest` keeps the first 63 characters of the digest. Older labels still deduplicate; an older controller does not recognise the new label
- **Example Pipelines** github-demo, flagger-demo, flux-demo and argo-rollouts-demo are named after their directories instead of `kardinal-test-app`. If you applied one, delete the old `kardinal-test-app` Pipeline and the old-named Kustomizations, Canary, Rollout and Application, and re-apply the example

### Removed

- **Distributed mode** — `kardinal-agent` and `--shard` / `KARDINAL_SHARD`. The controller reconciles every PromotionStep. `shard` on an environment sets the Pipeline `Ready=False`
- **Per-region fan-out** — `regions` and `PromotionStep.spec.region` are deprecated. Two or more regions fail at Graph build; declare one environment per region and use `wave`
- **The Pipeline admission webhook** (`--pipeline-admission-webhook`, `POST /webhook/validate/pipeline`). The controller exits at startup when it is set: remove the setting and delete your ValidatingWebhookConfiguration. Invalid Pipelines are marked `Ready=False` instead
- **The PromotionTemplate CRD and `PromotionStep.spec.inputs`**, and the custom `webhook`, `verify-image` and `integration-test` steps. No Pipeline could run them. Helm does not delete CRDs: run `kubectl delete crd promotiontemplates.kardinal.io --ignore-not-found` (only clusters that ran a build from `main` have it). For image signatures, use admission-time verification; for tests, Argo CD PostSync hooks or MetricCheck gates. See [Image signatures and tests](https://pnz1990.github.io/kardinal-promoter/pipeline-reference/#image-signatures-and-tests)
- **Never-written fields** — AuditEvent `spec.actor` and `spec.bundleImage`; Bundle `status.metrics.autoRollbacks`, `status.environments[].prMergedAt`, `.mergedBy` and `.gateResults`. The UI "CD Level" column, "Full CD" counter and `cdLevel` in `/api/v1/ui/pipelines` are gone (they counted `spec.policyGates`)
- **The EKS e2e Terraform** and `make eks-up`, `eks-down` and `setup-multi-cluster-env`. If you created `kardinal-e2e-prod` with them, destroy it from an older checkout

### Deprecated

- `PolicyGate.spec.when`, `spec.environments[].health.cluster` (still rejected: use an Argo CD or Flux hub), `regions`, `Pipeline.spec.git.provider` (ignored: the controller's `--scm-provider` / Helm `scm.provider` selects the provider), `kardinal rollback --emergency` (no effect: use `kardinal override`), `kardinal approve` (fails and points to `kardinal override`), and the chart values `rbac.integrationTestJobs` (no effect, removed in v0.10) and `validatingAdmissionPolicy.enabled` (no effect)

### Added

- **SCM providers** — Bitbucket Cloud and Azure DevOps (#1035, #1040)
- **UI API access control** — a static bearer token (`ui.auth.tokenSecretRef`) or Kubernetes TokenReview (`ui.auth.tokenReview`); CORS with `--cors-allowed-origins`; TLS with `--tls-cert-file` / `--tls-key-file`; the UI warns on an insecure connection (#924, #1015, #940, #937, #941)
- **NotificationHook CRD** — outbound webhooks on Bundle Verified, PolicyGate Blocked and PromotionStep Failed (#942)
- **`update.strategy: argocd`** — sets the image on the Argo CD Application without git operations (`argocd-set-image`, #966)
- **Create Bundle from GitHub Actions** — `.github/actions/create-bundle` (#953); the UI has a Create Bundle dialog (#950)
- **Pipeline and environment limits** — `maxConcurrentPromotions` (#1059), `stepTimeoutSeconds` (#1123), `waitForMergeTimeout` (#906, #908), `historyLimit` (#919)
- **`health.resource`** names the Deployment the resource adapter checks (#1117)
- **Chart** — `controller.watchNamespace` for a namespace-scoped install (#1024), `demo.enabled` (#1043), `grafanaDashboard.enabled` (#1139), `serviceMonitor.enabled` with `interval` and `labels` (#1268)
- **SCM token** — a changed token Secret is picked up without a restart (#994, #1060); the startup scope check runs in Helm installs too, in the background (#996, #1275). Bitbucket and Azure DevOps have no startup check
- **Metrics** — step duration, gate blocking time and PromotionStep age (#992); `/readyz` fails until the caches have synced (#1147)
- **CLI** — `get subscriptions` and a SUB column in `get pipelines` (#948); `logs` per-step table and `--follow` (#1012, #1124); `status` in-flight promotions (#997); `init --scaffold-gitops` and `--demo` (#1022); `delete bundle` (#851); `doctor` prints a version-pinned install command
- **`kubectl get` printer columns** for Bundle and PromotionStep (#903)
- **UI** — skeleton loading states (#784), `/` focuses the pipeline filter (#800), virtual scrolling over 50 pipelines (#817)
- **Examples** for every health adapter: resource, argocd, flux, argoRollouts and flagger (#821)

### Fixed

- Bundle requeue hot loop: a 1 ms `RequeueAfter` is now at least 500 ms (#988)
- `AbortedByAlarm` and `RollingBack` steps no longer reset to Pending (#789)
- The PR body's CI run cell no longer renders an empty link; an empty commit or author is `—`
- `kardinal validate` skips non-kardinal kinds and reports the same unimplemented fields as the Pipeline status
- The ClusterRole and Role grant access to NotificationHooks and AuditEvents (#1095)

### Release and CI

- Releases are cut only from tags on `main`. A prerelease gets no `latest` image tag and is not marked latest. The notes come from this changelog
- GitHub Actions are pinned to commit SHAs; kind, kubectl and argocd downloads are checked by sha256
- The web UI builds with npm only, and CI fails when the committed `web/dist` differs from a fresh build

---

## [v0.8.1] — 2026-04-17

**Security release: supply chain hardening — trivy, cosign, SBOM, SLSA, Graph controller image scan**

The v0.8.1 tag points to `bd2bcf3`, a merge commit that is not on main. Its tree is identical to `34d8524` (#787) on main.

### Added

- **Image vulnerability scan** — the release workflow scans the controller image with trivy and uploads the SARIF report; findings do not block the release (#696, #787). The Graph controller fork image is scanned too (#708)
- **Keyless image signing** — the controller image is signed with cosign through GitHub Actions OIDC (#700)
- **SBOM** — Syft SPDX SBOM, attached to the image as a cosign attestation (#703)
- **SLSA provenance** — GitHub build provenance attestation for the controller image (#707)
- **CLI** — `kardinal completion bash|zsh|fish|powershell` (#731); `kardinal validate` checks Pipeline and PolicyGate YAML offline (#713); `kardinal status` shows controller health and a resource summary (#715); `kardinal explain --color` (#730); `kardinal create bundle --dry-run` (#741); `explain` with an unknown environment lists the valid ones (#717); a dependency cycle error shows the cycle (#711)
- **Prometheus metrics** — `kardinal_bundles_total`, `kardinal_steps_total`, `kardinal_gate_evaluations_total`, `kardinal_pr_duration_seconds` (#726)
- **UI** — dark and light mode (#734); selection kept in the URL (#742); keyboard shortcuts `?`, `r`, `Esc` with a focus-trapped help modal (#750, #785); error boundaries with Retry (#755); copy-to-clipboard on pipeline names and bundle hashes (#764); the stale-data indicator turns red after 30 s (#767)
- **Demo environment** — complete demo setup (#786)

### Changed

- **UI theme tokens and WCAG 2.1 AA** — colors moved to CSS custom properties (#738) and meet 4.5:1 contrast in both themes (#760, #772); axe-core checks run in the Playwright suite (#756, #759, #771)

---

## [v0.8.0] — 2026-04-17

**Audit log, SCM circuit breaker, Bundle Watch node, Graph-first cleanup, DX improvements**

### Added

- **AuditEvent CRD** — an immutable promotion event log, with gate evaluation and rollback events (#679, #681)
- **`kardinal get auditevents`** (#684) and **`kardinal audit summary`** (#686)
- **Admission webhook** for Pipeline and Bundle validation (#670)
- **SCM circuit breaker** — exponential backoff that respects rate limits (#666)
- **Bundle Watch node** — `bundle.*` is in Graph CEL scope (#667)
- **Actionable CLI error hints** for common failures (#689)

### Changed

- **Graph controller fork pin** `81c5a03` → `05db829` (#677)

---

## [v0.7.0] — 2026-04-16

**WatchKind O(1) health checks, Graph controller fork 81c5a03 upgrade, reactive PromotionStep reconciler, graph-first cleanup**

### Added

- **WatchKind health nodes** — `health.labelSelector` on Pipeline environments switches from Watch (O(n) full list per event) to WatchKind (O(1) incremental cache). Requires Graph controller fork `745998f`+ (#652, docs #659)
- **Kargo migration guide** — concept mapping, side-by-side Pipeline vs Kargo YAML, 7-step migration walkthrough in `docs/guides/` (#640)
- **Operations runbook expanded** — PolicyGate debugging, SCM failure modes, RBAC issues, Graph controller restarts, performance tuning added (#639)
- **Bundle image diff in NodeDetail** — UI compares the current bundle's image against the previous bundle for that environment; closes a Kargo parity gap (#638)
- **Per-step progress observability** — `PromotionStep.status.steps[]` exposes each step with individual state, start time, and duration (#630)
- **`kardinal get pipelines --watch`** — real-time promotion progress with live table refresh (#629)
- **PrometheusRule CRD** — 6 pre-built alerting rules in Helm chart: promotion stuck, high rollback rate, policy gate blocked, SCM errors (#621)
- **UI: conditions summary and reason** — NodeDetail shows Kubernetes Conditions table with reason column; improved empty-state onboarding (#529, #530)
- **UI: Kubernetes events stream** — timestamped event history per PromotionStep in NodeDetail (#560)
- **UI: cross-environment error aggregation** — groups PromotionStep failures by type across environments; shows affected count (#564)
- **Graph controller fork upgraded to `745998f`** — Decorator bootstrap primitive, Definition compile-time type inference, forEach array format support, DAG finalizer guard for non-resource nodes (#614)

### Changed

- **PromotionStep `spec.upstreamStates`** replaces `spec.upstreamVerified` and `spec.upstreamVerified2`. The CRD declared only those two, so an environment with more than two upstream environments failed; the list has no limit. The Graph sets the field (#660)
- **Graph controller fork pin** `745998f` → `81c5a03` — health Watch nodes drop `readyWhen`, so the new fork does not patch the watched Deployment or Application; WatchKind nodes are scoped to the environment namespace (#654)

### Fixed

- **PromotionStep reacts to PRStatus and PolicyGate changes** — the reconciler watches both, so a merged PR or a gate that changes state moves the step on at once instead of at the next requeue (#655)
- **Bundle reconciler watches Pipeline changes** — Graph is regenerated when Pipeline spec changes (new environments, updated policyNamespaces, changed git config). Previously Pipeline changes were invisible to in-flight Bundles (#634)
- **Subscription deduplication under HA** — uses label selector (`kardinal.io/source-digest`) instead of status field comparison; safe under concurrent reconciles and multiple controller replicas (#636)
- **CEL documentation accuracy** — corrected false claims about `pkg/cel/NewCELEnvironment()` (does not exist) and `schedule.*` (map variable, not CEL library function) in design docs and code comments (#631)
- **`kardinal doctor`** — pre-flight cluster health check: validates CRD installation, the Graph controller, RBAC, and GitHub token before first use (#607)
- **Graceful shutdown** — controller drains in-flight reconcile loops on SIGTERM; no promotion steps interrupted by pod restarts (#605)
- **PodDisruptionBudget + topology spread** — minAvailable: 1 PDB and `topologySpreadConstraints` in Helm chart for HA deployments (#598)
- **Graph controller bundled in Helm chart** — single `helm install` installed both kardinal-promoter and the pre-upstream Graph controller fork (#590; reverted when kardinal moved to upstream kro, which is installed separately)
- **Library-based git operations** — replaced `exec.Command("git")` with `go-git` library (`#517`). Controller no longer requires a `git` binary. Improves portability (distroless images) and performance.
- Controller `/tmp` mount — `emptyDir` volume added for git-clone with `readOnlyRootFilesystem: true` (#609)
- `policy simulate` now searches all namespaces — org-level gates in `platform-policies` were never found
- `pkg/cel` standalone CEL evaluator eliminated — evaluation moved inline to PolicyGate reconciler
- Rollback PR title and body now include rollback notice and the `kardinal/rollback` label

---

## [v0.6.0] — 2026-04-14

**Live-cluster validation infrastructure, J7 multi-tenant self-service, OCI/Git source watchers, pipeline deployment metrics**

The v0.6.0 tag points to `369be4c`, a merge commit that is not on main. Its tree is identical to `a316253` (#515) on main.

### Added

- **Multi-tenant self-service (J7)** — ApplicationSet + Pipeline template bootstrap; team onboarding via Git directory; org PolicyGates automatically inherited (#489)
- **OCI + Git source watchers** — `OCIWatcher` and `GitWatcher` Subscription reconcilers poll registries and Git branches, creating Bundles on new images/commits (#491, #493)
- **Pipeline deployment metrics** — `Pipeline.status.deploymentMetrics` aggregated by `PipelineReconciler`: `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRate` (#498, #511)
- **`changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions** — named-argument helpers for ChangeWindow gates (#506)
- **ScheduleClock CRD** — writes `status.tick` on a configurable interval to drive time-based policy gate re-evaluation via real Kubernetes watch events; replaces the `ctrl.Result{RequeueAfter}` timer loop pattern (#484)
- **Graph controller fork upgraded to `948ad6c`** — DNS-1123 node ID validation, drift timers (30 min), propagation hash includes `propagateWhen` state
- **Cardinal logo** — added across docs site, UI sidebar, and README (#515)

### Changed

- **`kustomize-set-image` without the kustomize binary** — the step edits `kustomization.yaml` in Go. `kustomize-build` still runs the kustomize binary (#512)

### Fixed

- `kardinal-promoter` controller image rebuilt correctly when Graph CR is deleted externally (#490)
- PDCA live-cluster validation workflow: fixed pipeline name mismatch, missing `platform-policies` namespace, missing controller install step (#514)
- CI: `enforce_admins: true` on branch protection; 8 required status checks; concurrency guards on Docs and E2E workflows (#513)

---

## [v0.5.0] — 2026-04-13

**Pipeline Expressiveness (K-Series), Enterprise UI Control Plane, Graph controller upgrade**

### Added

- **K-01: Contiguous healthy soak** — `bake.minutes` + `bake.policy: reset-on-alarm` on environment spec; `BakeElapsedMinutes` and `BakeResets` tracked in PromotionStep status
- **K-02: Pre-deploy gate type** — `when: pre-deploy` on PolicyGate spec; holds the PromotionStep in `Pending` before `git-clone` starts
- **K-03: Auto-rollback with ABORT vs ROLLBACK distinction** — `onHealthFailure: rollback | abort | none` per environment
- **K-04: ChangeWindow CRD** — blackout and recurring allowed-hours windows; `changewindow["name"]` CEL map variable is `true` when the window is active/blocking (#460)
- **K-05: Bundle.status.metrics** — commitToProductionMinutes, bakeResets, autoRollbacks, operatorInterventions; `kardinal metrics` CLI command
- **K-06: Wave topology** — `wave: N` field on environment spec; Wave N automatically depends on all Wave N-1 stages
- **K-07: Integration test step** — built-in `integration-test` step runs a Kubernetes Job as part of the promotion sequence (#470)
- **K-08: PR review gate** — `bundle.pr["staging"].isApproved` and `.approvalCount` in CEL context via PRStatus CRD (#472)
- **K-09: `kardinal override` with audit record** — emergency gate override with mandatory reason + time limit; the override is recorded in the gate's `spec.overrides[]`, and the gate reason shows it in the PR evidence body (#471)
- **K-10: Cross-stage history CEL** — `upstream.<env>.soakMinutes`, `.recentSuccessCount`, `.recentFailureCount`, `.lastPromotedAt` in gate expressions (#473)
- **Policy test** — `kardinal policy test` checks PolicyGate YAML and CEL syntax offline (#235)
- **UI control plane** — all 7 UI issues shipped (#462–#468): fleet health dashboard, pipeline ops view, per-stage bake countdown, in-UI actions (pause/resume/rollback/override), release metrics bar, bundle timeline, policy gate detail panel

### Fixed

- Graph controller fork upgraded to `948ad6c` — DNS-1123 node ID validation, drift timers, propagation hash improvements
- `changewindow.isAllowed()` / `changewindow.isBlocked()` CEL helpers added alongside the map-style access

---

## [v0.4.0] — 2026-04-11

**Distributed Mode, Argo Rollouts delegation, graph purity, K-series features**

### Added

- **Argo Rollouts delivery delegation** — `delivery.delegate: argoRollouts` in Pipeline env spec hands off rollout progression to an existing `Rollout` resource (#197)
- **GitLab + Forgejo/Gitea SCM providers** — selected per controller with `--scm-provider gitlab` or `--scm-provider forgejo` (also `gitea`)
- **Graph purity milestone** — all 41 Graph-independent logic leaks eliminated (see `docs/design/11-graph-purity-tech-debt.md`)
- **K-01–K-11** — all Pipeline Expressiveness features (see v0.5.0 above for full list; initial implementation in this release)

### Fixed

- Pause enforcement via `bundle.status.paused` (eliminates in-memory pause state)
- Step cleanup on pipeline deletion no longer leaves orphaned PromotionSteps
- PolicyGate re-evaluation after TTL now fires correctly when graph resumes

---

## [v0.3.0] — 2026-04-11

**Observability: embedded UI, PR evidence, GitHub Actions**

### Added

- **Embedded React UI** — promotion DAG visualization with 6-state health chips, CEL expression display, live polling with staleness indicator, blocked-gate banner
- **Distributed mode** — `--shard` flag routes PromotionSteps to matching shard agents; supports multi-cluster deployments where each spoke cluster runs its own agent (#196)
- **PR evidence body** — structured markdown in every prod PR: image digest, CI run link, gate results, upstream soak time
- **kardinal diff** — `kardinal diff <bundle-a> <bundle-b>` shows artifact delta
- **kardinal approve** — approve a Bundle bypassing upstream gate requirements
- **kardinal metrics** — DORA-style promotion metrics (deployment frequency, lead time, fail rate)
- **kardinal refresh / dashboard / logs** — operational CLI commands
- **History command** — `kardinal history <pipeline>` shows previous promotions

### Fixed

- DAG graph deduplicates gate nodes (was showing duplicate PolicyGate cards)
- Pipeline phase uses `status.phase` not condition reason for display
- Explain command deduplicates gate output when multiple instances match

---

## [v0.2.1] — 2026-04-11

**Graph Purity: all Graph-independent logic leaks eliminated**

### Added

- **PRStatus CRD** — replaces in-reconciler GitHub API polling for PR state
- **RollbackPolicy CRD** — moves auto-rollback threshold logic out of PromotionStepReconciler
- **Health Watch nodes** — for each environment with `health.type`, the Graph watches the health resource (Deployment, Argo CD Application, Flux Kustomization, Argo Rollout, Flagger Canary) (#191, #194)
- **Promote command** — `kardinal promote` creates a Bundle from the last verified image (#160)
- **UI: 5s polling and bundle history** — the UI refreshes every 5 seconds and lists earlier Bundles of the selected pipeline (#170)

### Fixed

- **Promotion working directory** — the git working directory is recorded in `PromotionStep.status` and removed when the step finishes (#195)
- `kardinal policy list` shows `Pending` for a gate that was not evaluated yet, as `kardinal explain` does, instead of `unknown` (#170)
- `time.Now()` calls moved outside reconciler hot paths into CRD status writes
- Cross-CRD status mutations eliminated — each reconciler writes only to its own CRD
- `exec.Command()` in reconciler replaced with library call

---

## [v0.2.0] — 2026-04-11

**Workshop 1 Complete — first end-to-end validated release**

### Milestone

kardinal-promoter now executes the [AWS Platform Engineering on EKS workshop](https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo) end-to-end on a live kind cluster.

### Fixed

- kind E2E infrastructure (`make setup-e2e-env`) sets up the Graph controller + ArgoCD + test/uat/prod namespaces
- `kardinal get pipelines` shows per-environment status columns (#128)
- `kardinal explain` shows active PolicyGates with CEL expression and current value (#129)
- **Graph node IDs are CEL-safe** — node IDs use underscores, because CEL reads a hyphen as minus; the Kubernetes object names keep hyphens
- **git push** — pushes `HEAD:<branch>`, so a push no longer fails with `src refspec does not match any`
- **Git token from the Pipeline** — the token is read from the Secret in `Pipeline.spec.git.secretRef`
- **PromotionStep and PolicyGate schemas** — PromotionStep spec declares `upstreamVerified` and `requiredGates`, and PolicyGate spec declares `upstreamEnvironment`, the fields the Graph writes; without them the Graph controller rejected the objects
- **RBAC for health checks** — the ClusterRole can read Deployments, Argo CD Applications and Flux Kustomizations
- **Existing promotion PR** — when the PR is already open (for example after a controller restart), the controller finds it instead of failing with `422`
- **GitHub token in the Helm chart** — `github.secretRef` and `github.token` values pass `GITHUB_TOKEN` to the controller
- **Graph controller fork pin** upgraded to `9c18aa34`, which re-evaluates `propagateWhen` after the managed resource's status changes

---

## [v0.1.0] — 2026-04-10

**Foundation: CRDs, Controller, Graph Integration, PolicyGate**

### Added

- **Go module scaffold** — directory layout, Makefile, CI pipeline (build/lint/test/vet)
- **CRD types** — Pipeline, Bundle, PolicyGate, PromotionStep (kubebuilder markers, deep copy, validation)
- **Controller manager** — BundleReconciler, PipelineReconciler, PromotionStepReconciler, PolicyGateReconciler
- **Helm chart** — controller deployment, RBAC, CRDs packaged for OCI registry
- **Graph integration** — kro Graph builder and translator: Pipeline → Graph spec
- **PolicyGate CEL evaluator** — `!schedule.isWeekend`, `upstream.uat.soakMinutes >= 30`, kro CEL library
- **MetricCheck CRD** — Prometheus-backed policy gates: block promotions when error rate > threshold, with `metrics.<name>.result` and upstream soak time in gate expressions (#114)
- **Custom promotion steps** — HTTP webhook steps for extensible promotion workflows (#124)
- **Auto-rollback** — configurable failure threshold triggers rollback PR after N consecutive health failures (#77)
- **Pause/resume** — `kardinal pause/resume <pipeline>` halts in-flight promotions (#63, #110)
- **Policy simulate and list** — `kardinal policy simulate` evaluates gates without creating a Bundle; `kardinal policy list` lists the PolicyGates of a pipeline or environment (#63)
- **Config Bundle type** — promotes Git commit SHAs through the same pipeline as image Bundles (#78)
- **Rendered manifests step** — `layout: branch` with `kustomize-build` writes environment-specific YAML (#82)
- **SCM provider** — GitHub: push branch, open PR, detect merge, post comments
- **Health adapters** — Kubernetes Deployment readiness, ArgoCD Application sync, Flux Kustomization
- **Steps engine** — kustomize-set-image, helm-set-image, git-commit, open-pr, wait-for-merge, health-check
- **CLI foundation** — `kardinal get pipelines/bundles/steps`, `kardinal explain`, `kardinal rollback`, `kardinal version`, `kardinal init`
- **Embedded React UI** — scaffolded with Vite + React 19, embedded via `go:embed`

---

[Unreleased]: https://github.com/pnz1990/kardinal-promoter/compare/v0.8.1...HEAD
[v0.8.1]: https://github.com/pnz1990/kardinal-promoter/compare/v0.8.0...v0.8.1
[v0.8.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.7.0...v0.8.0
[v0.7.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.6.0...v0.7.0
[v0.6.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.5.0...v0.6.0
[v0.5.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.1...v0.3.0
[v0.2.1]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/pnz1990/kardinal-promoter/releases/tag/v0.1.0
