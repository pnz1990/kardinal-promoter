# Comparison: kardinal vs Kargo vs GitOps Promoter

This page compares kardinal-promoter with the two most similar tools in the GitOps promotion space.

!!! note "Objectivity"
    Surveyed on 2026-10-09 from each project's source, docs and release notes: kardinal-promoter
    v0.9.0 and its main branch (v0.10.0, unreleased), Kargo v1.12.3 and GitOps Promoter v0.45.0. All three tools are actively developed;
    check each project's releases for the latest capabilities. "Kargo Enterprise" means the
    commercial edition (Kargo on the Akuity Platform); everything else in the Kargo column is open source.

---

## Feature Matrix

| Feature | kardinal-promoter | Kargo | GitOps Promoter |
|---|---|---|---|
| **Promotion model** | DAG (fan-out, fan-in, arbitrary `dependsOn`) | DAG of Stages (a Stage takes Freight from one or more upstream Stages; `availabilityStrategy: All` for fan-in) | Ordered environment list, or a DAG with `dependsOn` (v0.38+) |
| **Parallel environments** | Yes — native fan-out + `wave:` topology | Yes — several Stages with the same upstream | Yes — `dependsOn` |
| **Policy gates** | PolicyGate CRDs with CEL: schedule, upstream soak and history, metrics, change windows, PR review state, Bundle metadata | No gate object. Verification (AnalysisTemplates), `requiredSoakTime`, manual Freight approval; expr-lang checks inside a promotion (`if:`, `fail`, `http`) fail it rather than hold it. Kargo Enterprise: promotion windows and `wait-for-approval` (v1.12, beta) | CommitStatus gates: Argo CD health, soak time, schedule windows, expr-lang checks on commits and web requests; any controller can write a CommitStatus |
| **Cross-stage policy** | Yes — a gate can read upstream soak, promotion history and PR approval state | Partial — Freight must be verified (and soaked) in the upstream Stage; a step can read it with `freightStatus()` (v1.12) | Partial — `DependentsSuccessfulCommitStatus` waits for upstream success; a `WebRequestCommitStatus` can read the whole PromotionStrategy status |
| **Pre-deploy gates** | Yes — a step stays Pending until every gate passes on a result newer than the step, before its first git or Argo CD write; not re-checked after the step starts | Freight availability (upstream verification and soak) before a Promotion is created; Kargo Enterprise promotion windows reject new Promotions, and a running one is not stopped | Yes — proposed commit statuses must pass before the PR merges, re-checked for every new commit |
| **PR evidence body** | Structured, in every PR kardinal opens (`approval: pr-review`): image provenance (digest, CI run, commit, author), each gate's result and reason, other environments' health-check times | The commit message plus a link to the Kargo UI; the `git-open-pr` description can be templated with expressions; no gate or verification results | Templated (v0.37+): deployed and proposed commits, a diff link, the promotion pipeline table; gate results appear as SCM checks, not in the body |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes (chosen per environment) | Argo CD (sync and health); can push Flux-compatible OCI artifacts (v1.12), with no Flux sync or health check | Argo CD first-class (Source Hydrator, health gate, UI extension); any engine that syncs the environment branches, with no health gate for it |
| **SCM providers** | GitHub (incl. Enterprise), GitLab (incl. self-managed), Forgejo/Gitea; Bitbucket Cloud and Azure DevOps (newer, less tested). One provider per controller | GitHub, GitLab, Gitea, Bitbucket Cloud, Azure DevOps | GitHub (a GitHub App, incl. Enterprise), GitLab, Forgejo, Gitea, Bitbucket Cloud, Azure DevOps |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts, Flagger | ArgoCD Application, plus AnalysisTemplate verification | ArgoCD Application (Healthy and Synced); others through a custom CommitStatus |
| **Rollback mechanism** | `kardinal rollback` promotes the previous artifact through the same pipeline, gates and PR flow | Re-promote older Freight; since v1.11 this pins the Stage so auto-promotion does not roll forward again | `RestoreActiveCommit` (v0.44): restores a version the environment already ran and blocks promotion there until released; otherwise a manual git revert |
| **Auto-rollback on health failure** | Yes — `onHealthFailure: rollback \| abort \| none` per stage, or a RollbackPolicy | Kargo Enterprise only (beta): back to the last verified Freight when verification fails | No |
| **Contiguous healthy soak** | Yes — `bake.minutes` resets timer on health alarm | No — `requiredSoakTime` counts time since the Freight was verified upstream; health does not reset it | No — `TimedCommitStatus` counts time since the commit merged; health does not reset it |
| **Change freeze management** | Yes — cluster-scoped `ChangeWindow` (blackout or recurring), enforced by any gate that references it | Kargo Enterprise only (promotion windows, v1.12, beta); open source ignores the field | `ScheduledCommitStatus` cron windows, global or per environment, per PromotionStrategy |
| **Pause in-flight promotions** | Yes — `kardinal pause` (CLI or UI): no new step starts, and a step still preparing its change holds before its next git step | Turn off auto-promotion per Stage; a manual re-promote pins the Stage (v1.11) | `autoMerge: false` per environment (PRs wait for a manual merge) |
| **Supersede an older release in flight** | Yes — a newer Bundle supersedes an older one that is still promoting | No — promotions queue ([#3108](https://github.com/akuity/kargo/issues/3108), its most-requested open issue) | Yes — the newest commit wins; intermediate commits may be skipped |
| **Wave topology** | Yes — `wave:` field generates multi-region DAG edges automatically | No (a Kargo Enterprise fan-out to fleets of targets with a concurrency limit is on its main branch, unreleased) | No |
| **CLI** | Full `kardinal` CLI incl. `explain`, `policy simulate`, `override`, `pause`, `rollback`, `metrics`, `logs`, `validate`, `status`, shell completion | `kargo` CLI (get, promote, approve, verify, grant, logs and more) | Minimal: `gitops-promoter dashboard`, `demo` and `version`; no operational commands |
| **Explain and simulate gates** | Yes — `kardinal explain` shows why an environment is blocked; `kardinal policy simulate` runs the gates at a chosen time | No | No (a Go package can simulate `WebRequestCommitStatus` expressions) |
| **UI dashboard** | Embedded UI: fleet board (the version each environment of every pipeline runs, and releases on their way), fleet health bar, ops table, pipeline lane and DAG, bundle timeline and comparison, policy gates with CEL expressions, metrics bar, step timings, dark and light themes; create bundle, pause/resume, promote and roll back from the UI (gate override is CLI-only) | Polished Kargo UI: pipeline graph, Freight timeline and diffs, drag-and-drop promotion, step logs | Read-only dashboard, plus an Argo CD UI extension; both show promotion history |
| **Metric-gated promotions** | Yes — `MetricCheck` CRD: Prometheus, Datadog, CloudWatch, New Relic, or any JSON API (`web`, JSONPath and a threshold); Secret-backed credentials; per-promotion queries templated with the Bundle version and environment ([Metric Checks](metric-checks.md)); no Argo Rollouts needed | Yes — verification with AnalysisTemplates (Prometheus, Datadog, CloudWatch, New Relic and others; needs Argo Rollouts installed) | Indirect — a `WebRequestCommitStatus` can call a metrics API |
| **DORA metrics** | Yes — all four: deployment frequency, lead time, change failure rate and time to restore in `Pipeline.status.deploymentMetrics`, `kardinal metrics` and the UI; per-step timings in `PromotionStep.status.steps` | No — operational Prometheus metrics only (v1.12) | No — on its roadmap ([#574](https://github.com/argoproj-labs/gitops-promoter/issues/574), its most-requested open issue) |
| **Distributed tracing** | Yes — OpenTelemetry over OTLP/HTTP, off by default: reconciles, promotion steps, git, SCM API calls, NotificationHook deliveries (with `traceparent`), inbound webhooks and Bundle API | On main, unreleased: OpenTelemetry tracing for control plane components ([kargo#7353](https://github.com/akuity/kargo/pull/7353), merged 2026-09-29); not in v1.12.1 | No |
| **Audit trail** | `AuditEvent` CRD (promotions, rollbacks, supersession, gate results) and Kubernetes Events | Kubernetes Events; `record-audit-event` step in Kargo Enterprise (v1.12) | `ChangeTransferPolicyHistory` (last 20 promotions per environment, v0.41), git notes, Kubernetes Events |
| **Custom promotion steps** | No — each environment runs a fixed sequence chosen by the Bundle type, `update.strategy` and `approval` ([Promotion Steps](pipeline-reference.md#promotion-steps)); `update.strategy: yaml` sets any YAML paths in several files (Kargo's `yaml-update`) | Yes — about 35 built-in steps composed in a Stage's `promotionTemplate`, reusable PromotionTasks, conditions and retries; container steps in Kargo Enterprise (v1.10+) | No |
| **Integration test step** | No — run tests as an Argo CD PostSync hook with `health.type: argocd`, or gate on a `MetricCheck` ([how](pipeline-reference.md#image-signatures-and-tests)) | Yes — verification can run a Kubernetes Job (AnalysisTemplate); an `http` step can poll a test service | No (a Job gate is on its roadmap) |
| **Image signature verification** | No — use admission-time verification in the workload cluster (Sigstore policy-controller or Kyverno `verifyImages`; [how](pipeline-reference.md#image-signatures-and-tests)) | No (Kargo Enterprise's `jfrog-evidence` step can verify signed JFrog evidence) | No (git commit signature checks are on its roadmap) |
| **Emergency gate override** | Yes — `kardinal override`: time-limited, with a mandatory reason; recorded in the gate's `spec.overrides`, a `GateEvaluated` AuditEvent and the PR evidence (the user name is the CLI's local OS user) | Manual Freight approval (`kargo approve`), with no reason or expiry | Merge the PR by hand; nothing is recorded |
| **Outbound event notifications** | Yes — `NotificationHook` CRD with native Slack (Block Kit) and Microsoft Teams (Adaptive Card) formats, JSON, or a templated body for any HTTP API; ten events (Bundle verified, failed, superseded, rollback started and done; gate blocked and unblocked; step failed, PR opened, waiting for approval); auth header and URL from a Secret; per-event dedupe key; controller egress allowlist; pipeline selector | Kargo Enterprise only: event routing to Slack, email or HTTP, and a `send-message` step (v1.8+). Open source emits Kubernetes Events, and an `http` step can call a webhook | Kubernetes Events only (a webhook CRD is in a draft PR) |
| **Multi-cluster** | Argo CD or Flux hub (health read from the hub's Applications or Kustomizations), or direct remote-cluster health through a kubeconfig Secret (`health.kubeconfigSecretRef`, inline credentials only) | Yes — through Argo CD; controllers can be sharded across clusters | Yes — the Argo CD gate reads remote clusters through kubeconfig Secrets |
| **Upstream soak time in gates** | Yes — `bundle.upstreamSoakMinutes >= 30` (minutes since the upstream was Verified) | Yes — `requiredSoakTime` (elapsed time) | Yes — a `TimedCommitStatus` on the upstream, which the ordering gate requires |
| **Cross-stage history in gates** | Yes — `upstream.<env>.recentSuccessCount`, `lastPromotedAt` | No | No |
| **Artifact discovery** | Bundle created by CI (HTTP API, GitHub Action) or the CLI; Subscription CRD polls public OCI registries and Git repos | Warehouse: images, Git and Helm charts, with webhook receivers for registries and SCMs | Git commits from a hydrator (Argo CD Source Hydrator or any other) |
| **Multi-artifact bundle** | Yes (image + config in one Bundle) | Yes (Freight, with creation criteria since v1.8) | No |
| **Architecture** | Graph-first (one kro Graph per Bundle) | Controllers over Stage and Freight CRDs; v2.0 will replace the storage layer | Controller, plus an optional API server for the dashboard |
| **Maturity** | v0.9.0, active development; runs on kro's alpha Graph API (v0.10.0-rc.0) | v1.12.3, production-grade; v2.0.0 is next | v0.45.0, `v1alpha1` API, "experimental" per its README |
| **License** | Apache 2.0 | Apache 2.0 | Apache 2.0 |

---

## What changed in 2026

Both projects closed some of the gaps this page used to list.

**Kargo** (v1.8 to v1.12):

- Promotion windows, `wait-for-approval` with approver quorum, and audit events, in Kargo Enterprise (v1.12, beta). Open source ignores the windows.
- Auto-rollback to the last verified Freight, in Kargo Enterprise (beta).
- Auto-promotion holds (v1.11): a manual re-promote of older Freight pins the Stage.
- Freight creation criteria and the `MatchUpstream` selection policy (v1.8), a REST API with API tokens and a generic webhook receiver (v1.9), and its own Prometheus metrics (v1.12).
- Notifications to Slack, email and HTTP, in Kargo Enterprise (v1.8+).
- Admin login logging, with optional source IP and a log of every request (v1.12.2).
- Next: v2.0.0. Its main branch moves Projects and Targets into a database served by the API server, adds a Bitbucket Data Center provider, and removes deprecated tag filters and SSH URLs. A Kargo Enterprise fan-out to fleets of targets is also on main, unreleased.

**GitOps Promoter** (v0.13 to v0.42):

- DAG promotion with `dependsOn` and the required ordering gate (v0.38, v0.39). It now runs fan-out and fan-in like kardinal and Kargo.
- `ScheduledCommitStatus` schedule windows (v0.34), `WebRequestCommitStatus` gates (v0.23) and `GitCommitStatus` expressions (v0.19).
- An enriched default PR description (v0.37) and a promotion history CRD (v0.41).
- Bitbucket Cloud, Azure DevOps and Gitea (v0.19, v0.20).
- A redesigned dashboard and Argo CD UI extension with promotion history (v0.25 to v0.40).
- `RestoreActiveCommit` (v0.44): roll one environment back to a version it already ran, and block promotion there until released.
- Windows CLI binaries, and SCM rate-limit metrics per GitHub App installation (v0.45).
- Open PRs: outbound notification webhooks, a namespaced mode, a Bitbucket Data Center provider and runtime UI plugins.

Still unique to kardinal: CEL gates that combine schedule, soak, history, metrics and change windows;
the contiguous healthy soak; wave topology; gate results and provenance in the PR body; DORA metrics;
`explain` and `policy simulate`; and auto-rollback in open source.

Open requests in their trackers that kardinal already ships:

- Kargo: superseding queued promotions ([#3108](https://github.com/akuity/kargo/issues/3108)), time windows in open source ([#1328](https://github.com/akuity/kargo/issues/1328)), a system-wide stop ([#1422](https://github.com/akuity/kargo/issues/1422)), and provenance in PR bodies ([#2830](https://github.com/akuity/kargo/issues/2830)).
- GitOps Promoter: DORA metrics ([#574](https://github.com/argoproj-labs/gitops-promoter/issues/574)) and holding a promotion ([#1803](https://github.com/argoproj-labs/gitops-promoter/issues/1803)).

---

## Why kardinal-promoter

### Graph-native policy evaluation

kardinal PolicyGates are nodes in the kro DAG. A gate's CEL expression reads the Bundle, the
schedule, upstream soak and promotion history, metrics and change windows, not just the current stage:

```yaml
# In a prod PolicyGate — reads upstream uat stage's soak time
expression: "bundle.upstreamSoakMinutes >= 60"

# Read a MetricCheck result
expression: 'metrics["error-rate"].result == "Pass"'

# Combine schedule + soak + metadata
expression: '!schedule.isWeekend && bundle.upstreamSoakMinutes >= 30 && !(has(bundle.labels.hotfix) && bundle.labels.hotfix == "true")'
```

Kargo (`requiredSoakTime`) and GitOps Promoter (`TimedCommitStatus`) can wait a fixed time
after the upstream promotion. Neither resets the timer on a health alarm, or combines soak
time with schedule, metrics and Bundle metadata in one expression. Both use expr-lang
elsewhere: Kargo in promotion steps, which fail a promotion rather than hold it, and
GitOps Promoter in its commit and web-request gates.

### Structured PR evidence

Every PR kardinal opens (environments with `approval: pr-review`) has a body like this
(`pkg/scm/pr_template.go`; see [PR Evidence](pr-evidence.md)):

```markdown
## Promotion: my-app-x7k2p -> my-app/prod

### Artifact Provenance

| Image | Tag | Digest | CI Run | Commit SHA | Author |
|---|---|---|---|---|---|
| ghcr.io/myorg/my-app | 1.29.0 | sha256:a1b2c3d4 | [CI run](https://github.com/myorg/my-app/actions/runs/12345) | abc123d | engineer-name |

### Policy Gate Compliance

| Gate | Namespace | Result | Reason | Last Evaluated |
|---|---|---|---|---|
| no-weekend-deploys | my-app | Pass | bundle.version=1.29.0: !schedule.isWeekend = true | 2026-04-14T14:00Z |

### Upstream Verification

| Environment | Health Checked At | Elapsed |
|---|---|---|
| staging | 2026-04-14T13:15Z | 45m |
```

Kargo's default PR body is the commit message and a link to the Kargo UI. You can template the
description with expressions, but Kargo adds no gate or verification results. GitOps Promoter's
default template lists the deployed and proposed commits, a diff link and the promotion pipeline
table; its gate results appear as SCM checks, not in the body.

### Contiguous healthy soak

`bake.minutes` counts *contiguous* healthy minutes — if a health alarm fires during the
soak window, the timer resets to zero. The deployment must survive a full `bake.minutes`
window with no alarms. Kargo's `requiredSoakTime` counts time since the Freight was verified
in the upstream Stage, and GitOps Promoter's `TimedCommitStatus` counts time since the commit
merged. Neither resets when the service turns unhealthy.

### Change freeze management

A `ChangeWindow` is a cluster-scoped object: a one-off blackout (`start`/`end`) or a
recurring allowed window (days, hours, timezone). Platform teams reference it from org-level
PolicyGates (`!changewindow.isBlocked("freeze")`, one gate per environment name such as `prod`),
so one object blocks every pipeline's matching environment during incidents, holidays, or
maintenance windows, with no per-pipeline changes. The freeze stops steps that have not
started yet. Kargo open source has no equivalent; Kargo Enterprise has promotion windows
(v1.12, beta), checked when a Promotion is created. GitOps Promoter has `ScheduledCommitStatus`,
set up per PromotionStrategy.

### Wave topology for multi-region rollouts

The `wave:` field on Pipeline environments generates DAG dependency edges automatically:
wave 2 cannot start until all wave 1 stages are verified. This makes the prod-wave-1 →
prod-wave-2 → prod-wave-3 pattern idiomatic in three lines of YAML. Kargo and GitOps
Promoter have no wave concept. Kargo's main branch has an unreleased Kargo Enterprise
fan-out to fleets of targets with a concurrency limit.

### DORA metrics built-in

`Bundle.status.metrics` records `commitToProductionMinutes`, `bakeResets` and `operatorInterventions`
(the `kardinal override` entries on the Bundle's gates) for every promotion.
`Pipeline.status.deploymentMetrics` adds rollouts in the last 30 days, p50 and p90 lead time,
the auto-rollback and operator intervention rates, and the change failure rate and mean time to
restore over the last 30 deployments to the last environment. Lead time starts when the Bundle is created.
The `kardinal metrics` CLI surfaces these per pipeline. Kargo v1.12 added operational Prometheus
metrics, not DORA metrics; GitOps Promoter lists DORA metrics on its roadmap.

### Auto-rollback and health-failure policy

With `onHealthFailure: rollback` (the default is `none`), kardinal creates a rollback Bundle
when a promotion fails health verification. Each stage can independently configure `onHealthFailure: rollback | abort | none`.
Combined with `bake.policy: fail-on-alarm`, this gives fine-grained control: critical stages
abort and require human intervention; non-critical stages roll back automatically.
GitOps Promoter has no automated rollback. Kargo has it only in Kargo Enterprise (beta),
triggered when verification fails.

### Fan-out DAG topology

```
test ──► uat ──► staging ──┬──► prod-us ──┐
                           └──► prod-eu ──┴──► verified
```

All three tools can run this. kardinal models it natively, and `wave:` can generate the edges.
Kargo builds it from Stages that request Freight from upstream Stages. GitOps Promoter builds
it with `dependsOn` on its environments.

---

## Where Kargo or GitOps Promoter is ahead

- **Composable promotion steps** (Kargo). About 35 built-in steps (git, Helm, Kustomize, YAML,
  OCI, HTTP, Argo CD), reusable PromotionTasks, conditions and retries. kardinal runs a fixed
  sequence per environment.
- **Artifact discovery** (Kargo). Warehouses watch images, Git and Helm charts, including private
  ones, with webhook receivers for registries and SCMs. kardinal's Subscription polls public
  registries and repos only; create Bundles from CI for private ones.
- **Verification providers** (Kargo). AnalysisTemplates query Prometheus, Datadog, CloudWatch,
  New Relic and others, and can run a Job. A kardinal `MetricCheck` covers Prometheus, Datadog,
  CloudWatch, New Relic and JSON web APIs, per promotion with `perPromotion` (unreleased), but
  not Jobs or the long tail of AnalysisTemplate providers (Wavefront, Graphite, InfluxDB, Kayenta).
- **Access control and API** (Kargo). Projects with per-project roles, OIDC claim mapping,
  API tokens and a REST API.
- **Gates mirrored to the SCM** (GitOps Promoter). Commit statuses appear as SCM checks and are
  re-checked for every new commit until the PR merges. A kardinal gate is checked before the
  step starts, not after.
- **Pluggable gates** (GitOps Promoter). Any controller can write a `CommitStatus`.
- **Monorepos** (GitOps Promoter). Several PromotionStrategies can share one environment
  branch (`activePath`).

## Known limitations

kardinal-promoter is the right tool for most of what is described above. There are cases
where a different approach may be a better fit — not because the competition is better,
but because the use case doesn't match what kardinal is designed for.

**You want native ArgoCD updates with a PR review.**
`update.strategy: argocd` patches the Argo CD Application directly, with no git commit (the
equivalent of Kargo's `argocd-update`), but it supports only `approval: auto`: the API server
rejects a Pipeline that sets `approval: pr-review` on the same environment. It promotes image
Bundles only. See [ArgoCD-Native Promotion](argocd-native-promotion.md).

**You need custom promotion steps.**
kardinal runs a fixed step sequence per environment. If each environment needs its own
build, render or script steps, Kargo's promotion templates fit better.

**You want zero state outside Git.**
kardinal maintains state in Kubernetes CRDs (Pipeline, Bundle, PromotionStep, AuditEvent and others).
If your constraint is that every promotion artefact must be a git commit with no
Kubernetes-side state, kardinal is not the right model.

**You need a larger community and commercial support today.**
kardinal-promoter is at v0.9.0 with active development, on kro's alpha Graph API. Kargo has a
longer production track record and commercial backing from Akuity. If your organisation requires vendor
support or a larger existing community before adopting, that is a legitimate constraint.

**Your only gate requirement is "a human clicks approve."**
kardinal's DAG, CEL gates, and structured evidence add meaningful complexity. If your
promotion workflow is simply "CI passes, human approves PR," that complexity is overhead
you don't need.

---

## When to choose kardinal-promoter

- You want **expressive, cross-stage policy gates** — soak time, upstream history, metrics, schedule, bundle metadata, PR approval state — without writing webhook servers
- You need **contiguous healthy soak** — deployments must survive bake windows with zero health alarms, not just elapsed time
- You want **wave topology** for multi-region production rollouts — promote to 1 region, bake, then expand to the next wave
- You want a **centralized change freeze** — one `ChangeWindow` object, referenced by org-level gates, blocks every pipeline during incidents or holidays
- You use **ArgoCD + Flux mixed**, or neither — kardinal doesn't require a specific GitOps engine
- You want **structured PR evidence** so reviewers have full promotion context in the PR body
- You want **auto-rollback** triggered by health check failures, with per-stage abort vs. rollback vs. ignore policy, in open source
- You want a **newer release to supersede an older one** still in flight, and to **pause** a pipeline mid-promotion
- You want to **see why a promotion is blocked** (`kardinal explain`) and test a policy before it applies (`kardinal policy simulate`)
- You are a **platform team** that needs org-level policies automatically applied to all pipelines, which a team Pipeline cannot remove (break-glass overrides are time-limited and recorded, and need `patch` on PolicyGates in the Pipeline namespace)
- You want **DORA metrics** — time-to-production, rollback rate, operator interventions — surfaced per pipeline
- You need a **time-limited emergency override** that records its reason on the gate and in the PR evidence, not a silent bypass

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](faq.md) — common questions
- [Migrating from Kargo](guides/migrating-from-kargo.md) — concept mapping and a migration walkthrough
