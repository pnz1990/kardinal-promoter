# Design: Graph Purity — Architecture Tech Debt Tracker

> Status: Active — every logic leak is tracked here
> Related: `docs/design/10-graph-first-architecture.md`
> Last audited: 2026-04-14

---

## Agent Instructions

**Read this document at the start of every queue generation. It overrides any other scope.**

### Milestone v0.2.1: COMPLETE

All 41 krocodile-independent logic leaks have been eliminated (issues #131–#155 resolved).
The v0.2.1 queue is closed. Do not re-open these items.

### What to work on now

Active open items are tracked in GitHub issues. Check the current open issue list.
The remaining logic leaks require either:
1. krocodile upstream changes (labeled `blocked-on-krocodile`) — do not workaround
2. Large architectural work: flat DAG compilation (#496), go-git migration (#495), kustomize library migration (#494)

### Hard rule: no new logic leaks

**Any PR that introduces logic outside the Graph layer (a new `time.Now()`, a new external HTTP call in a reconciler, a new cross-CRD mutation, a new CEL evaluation outside `pkg/reconciler/policygate`) requires explicit human approval before merging.**

QA must block such PRs with `[NEEDS HUMAN]`. Engineers must not implement them. This is Constitution Article XII.

---

## The Vision

In a perfectly pure architecture, kardinal-promoter is **pure YAML** from the user's perspective. No custom Go logic exists except:

1. **Owned node reconcilers** that compute a value and write it to `status.ready` — visible to the Graph
2. **CEL library extensions** on krocodile's Graph environment — stateless, synchronous, pure functions
3. **CLI** that reads CRDs and creates CRDs — no business logic, no API calls

Everything else is the Graph. The Graph handles sequencing, fan-out, fan-in, conditional inclusion, and teardown. All business rules are expressed as `readyWhen` / `propagateWhen` / `includeWhen` CEL expressions on Graph nodes.

**The world is a DAG. Everything is a Graph node.**

---

## One Permitted Exception (Transitional)

`pkg/cel/` is a documented transitional workaround. See `docs/design/10-graph-first-architecture.md` §Known Exceptions. It must not grow. It will be deleted once `recheckAfter` lands in krocodile.

---

## Fixable Without Krocodile (Milestone v0.2.1 — COMPLETE)

All 41 fixable leaks below were resolved in v0.2.1. Issues #131–#155 are closed.
This section is preserved for historical reference.

### CRITICAL (fix first)

| ID | Issue | Description | Fix Approach |
|---|---|---|---|
| CEL-2 / PG-2 | #131 | `buildMetricsContext()` aggregates MetricCheck CRDs in Go | Create MetricCheck Watch node; remove Go aggregation |
| PS-4 / SCM-2 / ST-10 / ST-11 / BU-3 / WH-1 | #133 | GitHub API `GetPRStatus()` in 5 code paths | New `PRStatus` CRD + reconciler; Watch node replaces all 5 |
| PS-6 / PS-7 | #134 | Auto-rollback threshold in Go; Bundle created from PromotionStep reconciler | New `RollbackPolicy` CRD; threshold is Watch node condition |
| ST-3 / ST-4 | #135 | CustomWebhookStep blocks reconciler with `time.After` | Replace blocking retries with `ctrl.Result{RequeueAfter}` |
| CLI-1 / CLI-2 / CLI-3 | #137 | CLI imports `pkg/cel`; schedule.isWeekend computed client-side | Server-side simulation API; remove `pkg/cel` from CLI |

### HIGH

| ID | Issue | Description | Fix Approach |
|---|---|---|---|
| PG-3 | #133 | `buildUpstreamContext()` soakMinutes via `time.Since` | Add `status.soakMinutes` to PromotionStep; Watch node reads it |
| PS-2 / BU-2 | #139 | `Pipeline.Spec.Paused` in two reconcilers | Single freeze-gate pattern (already exists); remove Go checks |
| PS-5 | #140 | Health check timeout via `time.Since` | Add `status.healthCheckExpiry` to PromotionStep |
| PS-9 | #141 | `copyEvidenceToBundle()` cross-CRD mutation | Invert: Bundle reconciler reads PromotionStep status |
| HE-4 | #143 | `AutoDetector` CRD probing at runtime | Remove AutoDetector; require explicit `health.type` |
| ST-5 / ST-6 | #144 | `exec.Command("kustomize")` in reconcile path | Use `kyaml`/`sigs.k8s.io/kustomize` library; no binary deps |
| ST-7 / ST-8 / ST-9 / SCM-5 | #144 | `git` host-local operations | Use `go-git` library; no shell-out; add `status.workdir` |
| GB-2 | #145 | `validateSkipPermissions()` at Graph-build time in Go | Move to Graph `includeWhen` expression |
| BU-1 / BU-4 | #146 | `supersedeSiblings()` in Go loop | Dedicated supersession reconciler watching Pipeline.status |
| WH-1 / WH-2 | #147 | Reconciler work in HTTP handler; triplicated URL parsing | Webhook only writes PRStatus CRD; consolidate URL parsing |

### MEDIUM

| ID | Issue | Description | Fix Approach |
|---|---|---|---|
| PG-5 / PG-6 | #148 | Template/instance distinction; extractVersion() not in CRD | Write results to CRD status fields |
| PS-3 | #149 | Shard filtering silent skip in Go | Use label selector on controller; add to CRD spec |
| PS-8 / HE-5 | #143 | Hardcoded naming conventions; live CRD probe on hot path | Move to Pipeline spec; cache at startup |
| GB-1 | #150 | Sequential default not in Pipeline spec | Add `sequentialDefault: true` field |
| TR-1 / TR-2 | #151 | `collectGates()` namespace aggregation; `policyNS` hardcoded | Add `policyNamespaces` to Pipeline spec |
| CLI-3 / CLI-4 / CLI-5 | #152 | Gate filtering reimplemented; rollback assumes image type | Use server API; read type from CRD |
| BU-4 / WH-2 | #153 | Type-aware supersession; triplicated URL parsing | Explicit in spec; shared function |

### LOW

| ID | Issue | Description | Fix Approach |
|---|---|---|---|
| SCM-3 / SCM-4 | #154 | `EnsureLabels` on hot path; `time.Since` in template | Setup reconciler; pre-computed CRD field |
| CLI-7 / MC-1 | #155 | PolicyGate three-way state in CLI; threshold Go enum | Add `status.phase` to PolicyGate; CEL expression field |

---

## Blocked on Krocodile

**Nothing is currently blocked on krocodile.** All previously blocked issues have implementation
paths that require only kardinal changes. See §Previously Blocked — Now Unblocked below.

`recheckAfter` as a krocodile primitive is still desirable for the general case and should
be contributed upstream — but kardinal no longer requires it as a prerequisite for any feature.

| ID | Issue | Desired krocodile contribution | Priority |
|---|---|---|---|
| PG-1 / PG-4 | #138 | `recheckAfter` on Graph nodes | Nice-to-have — superseded by `ScheduleClock` pattern |
| GB-5 | #138 | Explicit `dependsOn` edges | Nice-to-have — positional workaround is correct today |
| HE-1 / HE-2 / HE-3 | #136 | `ShapeWatch` for external K8s resources | Superseded by Aggregated API (#456) |

---

## Previously Blocked — Now Unblocked

### #138 (recheckAfter): unblocked via ScheduleClock pattern

**Ellis Tarn (krocodile author) suggested using `propagateWhen` with a time-based trigger node.**

The `ScheduleClock` CRD is an Owned node whose sole job is writing `status.tick = time.Now()`
on a configurable interval. This fires a real Kubernetes watch event. PolicyGate nodes that
reference `clock` in their dependency scope re-evaluate their `propagateWhen` expressions on
every tick — including `schedule.isWeekend()` and `schedule.hour()` functions.

Register `schedule.*` as CEL library extensions on the Graph's `DefaultEnvironment` (Q3 — 
stateless, cheap, synchronous). No `recheckAfter` krocodile primitive required.

See §ScheduleClock Implementation below for the full spec.

### #132 (step-as-Graph-node): unblocked via flat DAG compilation

See §Flat DAG Compilation below. Bundle type is known at creation time — the full step DAG
can be generated statically. No runtime Graph mutation. No krocodile changes required.
Performance at scale is a benchmark question, not a correctness blocker.

### #130 and #68 (eliminate pkg/cel): fully unblocked

With `ScheduleClock` + `schedule.*` CEL library (from #138 solution), `pkg/cel` can be deleted
entirely:
- Non-time-based expressions → Graph Watch nodes (no krocodile needed)
- Time-based expressions (`schedule.*`) → CEL library on Graph DefaultEnvironment + ScheduleClock trigger

### #400 (Journey 2 multi-cluster): unblocked via Stage 14 implementation

Was labeled blocked via Stage 14 → #132 → krocodile. Since #132 is unblocked, Stage 14 is
a kardinal implementation task. Journey 2 test can be written once Stage 14 ships.

---

## ScheduleClock Implementation — #138 Unblocked

> Closes: #138, #130, #68 (eliminates `pkg/cel` entirely)
> Suggested by: Ellis Tarn (krocodile author)
> Architecture: ✅ Pure — Q2 (Owned node) + Q3 (CEL library extension)

### Problem

Time-based PolicyGate expressions (`!schedule.isWeekend()`, `schedule.hour >= 9`) need
periodic re-evaluation. Nothing in the cluster changes when the clock advances — no watch
event fires, so the Graph never re-evaluates these nodes.

### Solution

**Two parts that compose:**

**1. `ScheduleClock` CRD** — an Owned node that writes a timestamp on a fixed interval,
generating real Kubernetes watch events:

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// pkg/reconciler/scheduleclock/reconciler.go
// Reconciler writes status.tick every spec.interval.
// This is the only thing it does — it exists to generate watch events.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var clock kardinalv1alpha1.ScheduleClock
    if err := r.Get(ctx, req.NamespacedName, &clock); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    clock.Status.Tick = time.Now().UTC().Format(time.RFC3339)
    if err := r.Status().Update(ctx, &clock); err != nil {
        return ctrl.Result{}, fmt.Errorf("updating tick: %w", err)
    }
    interval := clock.Spec.Interval.Duration
    if interval == 0 {
        interval = time.Minute
    }
    return ctrl.Result{RequeueAfter: interval}, nil
}
```

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ScheduleClock
metadata:
  name: kardinal-clock
  namespace: kardinal-system
spec:
  interval: 1m
status:
  tick: "2026-04-13T14:00:00Z"   # updated every interval
```

**2. `schedule.*` CEL library on Graph DefaultEnvironment** — stateless functions:

```go
// pkg/cel/schedule/library.go
// Registered on the Graph's DefaultEnvironment via WithCustomDeclarations.
// schedule.isWeekend() — true if Saturday or Sunday UTC
// schedule.hour()      — current UTC hour (0-23)
// schedule.dayOfWeek() — "Monday", "Tuesday", ...
```

**3. Graph builder wires clock dependency** — any PolicyGate node whose expression
contains `schedule.` gets an automatic data-flow reference to the `ScheduleClock` node:

```yaml
# Generated by the Pipeline translator for a PolicyGate with schedule.* expression
- id: noWeekendDeploys
  template:
    apiVersion: kardinal.io/v1alpha1
    kind: PolicyGate
    ...
  propagateWhen:
    - "${!schedule.isWeekend() && kardinal_clock.status.tick != ''}"
    #                              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
    #  clock reference creates a data-flow edge — re-evaluated on every tick
```

### What this eliminates

- `pkg/cel/environment.go` — deleted entirely (#130, #68)
- `ctrl.Result{RequeueAfter: N}` timer loop in PolicyGate reconciler
- `pkg/cel/` import in CLI (`cmd/kardinal/policy.go`) — CLI calls server API instead
- All `time.Now()` / weekday/hour computation in `policygate/reconciler.go`

### One ScheduleClock per cluster is sufficient

All pipelines share one `kardinal-clock` in `kardinal-system`. The 1-minute tick interval
is appropriate for time-based gates — gates that care about business hours don't need
sub-minute precision.

---

## Flat DAG Compilation — #132 is Unblocked

> This section documents the resolution of why #132 was labeled blocked-on-krocodile and
> establishes the correct implementation approach.

### The original concern

Issue #132 was labeled `blocked-on-krocodile` citing two concerns:
1. Large node counts degrading the Graph controller
2. Dynamic step sequences requiring runtime Graph mutation

### Why it is not actually blocked

**Dynamic node count is not the problem.** The Graph is generated *per-Bundle* at Bundle creation time. At that point the Bundle type is known (image, config, helm), so the complete flat step DAG can be generated statically in the Pipeline translator. The resulting Graph is immutable for that Bundle's lifetime — correct behavior, no runtime mutation needed.

**Large node counts are a performance concern, not a correctness blocker.** A 5-environment pipeline with 7 steps per environment = 35 Graph nodes. krocodile handles this today. Benchmark before optimizing, not before building.

### The correct implementation

At Bundle creation, the Pipeline translator generates a **fully flat DAG** where each step is a Graph node:

```
# For one environment "prod" with image Bundle:
GitCloneTask(prod)      → SetImageTask(prod)
SetImageTask(prod)      → GitCommitTask(prod)
GitCommitTask(prod)     → GitPushTask(prod)
GitPushTask(prod)       → OpenPRTask(prod)
OpenPRTask(prod)        → WaitForMergeTask(prod)   [Watch on PRStatus]
WaitForMergeTask(prod)  → HealthCheckTask(prod)    [Watch on Deployment/App/Kustomization]
HealthCheckTask(prod)   → [next environment's GitCloneTask, or final verified node]
```

Step type selection (`kustomize-set-image` vs `helm-set-image` vs `config-merge`) is encoded as `includeWhen` conditions on Graph nodes — config Bundle includes `ConfigMergeTask`, excludes `SetImageTask`, etc. The Graph controller handles the conditional inclusion natively.

**CRD design decision (resolved):** Use a generic `PromotionStepTask` CRD with a `type` field, not separate CRD types per step. Rationale: RBAC, observability, and reconciler registration are simpler with one CRD. The `type` field drives behavior.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PromotionStepTask
metadata:
  name: prod-git-clone-abc123
  labels:
    kardinal.io/bundle: abc123
    kardinal.io/environment: prod
    kardinal.io/step: git-clone
spec:
  type: git-clone           # git-clone | set-image | git-commit | git-push |
                            # open-pr | wait-merge | health-check | config-merge |
                            # helm-set-image | custom-webhook
  bundleRef: abc123
  environmentRef: prod
  config: {}                # step-specific config from Pipeline spec
status:
  phase: Pending | Running | Succeeded | Failed
  outputs: {}               # passed to downstream steps via Graph scope
  startedAt: ""
  completedAt: ""
```

### What this eliminates

Completing #132 with the flat DAG approach eliminates these logic leaks in one PR:
- PS-1: `DefaultSequenceForBundle()` Go routing algorithm
- PS-10: `StepState.Outputs` in-memory map (step outputs become CRD status fields in Graph scope)
- ST-1: `DefaultSequenceForBundle()` in steps package
- ST-2: `Engine.ExecuteFrom()` mini-scheduler
- ST-5 through ST-12: all `exec.Command` calls move to dedicated reconcilers per step type

### Performance note

Before implementation, benchmark krocodile with a synthetic Graph of 50+ nodes to confirm no
controller performance regression. If a regression is found, report upstream with the benchmark
data — do not work around it by keeping the Go step engine.

---

## Aggregated API Adoption Plan (ellistarn/kro#80)

> This section tracks the planned adoption of krocodile's aggregated API design for
> external system integration. See: https://github.com/ellistarn/kro/pull/80

### What it is

A design by the krocodile author for serving external system state as native Kubernetes
resources via the Kubernetes API Aggregation Layer. The first provider is GitHub (`api.github.com`),
exposing `GithubArtifact` and `GithubAuthentication` resources. Graphs consume these through
existing Watch semantics — no new krocodile primitives required.

Key properties:
- **No CRD sync drift** — state is served live via aggregated apiserver, not copied into CRDs
- **Request deduplication** — N Graphs watching the same GitHub path produce one upstream API call
- **ETag-based caching** — `If-None-Match` avoids counting polls against rate limits
- **Content-hash resourceVersion** — watch events fire only when data actually changes
- **OAuth device flow** — no PAT management; `kubectl get githubartifacts` shows the auth URL

### What this eliminates in kardinal

When this lands in krocodile, kardinal should refactor the following **in a single PR**:

| Logic leak | Current (purity violation) | With aggregated API (pure) |
|---|---|---|
| `GetPRStatus()` in reconciler hot path (#133) | Live GitHub API call in 5 code paths | Watch node on `GithubArtifact` for the PR branch |
| `PRStatus` CRD reconciler (#133) | kardinal-owned reconciler calls GitHub | Replaced by `GithubArtifact` Watch node |
| `git clone` in step engine (#140) | `exec.Command("git clone")` | Watch node on `GithubArtifact` for repo path |
| `EnsureLabels()` repo config (#149) | GitHub API call in promotion path | One-time `GithubArtifact` setup Watch node |
| `PAT-in-Secret` auth model | User manages PAT lifecycle | OAuth device flow via `GithubAuthentication` |
| Subscription CRD polling (#18 planned) | Polling reconciler with `time.After` | Watch node on `GithubArtifact` where `status.sha` changes → create Bundle |

This single aggregated API adoption PR would close issues **#128, #133, #140, #143, #149**
and unblock the Subscription CRD implementation as a clean Watch node.

### Migration path

When `ellistarn/kro#80` merges into krocodile mainline:

1. Deploy the `github-provider` aggregated API server alongside the kardinal controller
   (ship as an optional component in the kardinal Helm chart — off by default, enabled via
   `github.provider.enabled: true`)
2. Replace `PRStatus` CRD + reconciler with a Watch node on `GithubArtifact`
3. Replace `git clone` exec.Command with Watch node on `GithubArtifact` for repo reads
4. Remove `pkg/scm/github.go` API call paths; the aggregated API server handles all GitHub I/O
5. Update `kardinal init` to guide users through OAuth device flow instead of PAT creation
6. Implement Subscription CRD as a Graph watching `GithubArtifact` for SHA changes

**Do not implement the `PRStatus` CRD workaround (#133) if the aggregated API is imminent.**
If `ellistarn/kro#80` is within 2–3 milestones of merging, skip the workaround and wait for
the clean path. If it stalls, implement `PRStatus` CRD as a transitional measure and migrate
when the aggregated API lands.

### GitLab provider

The aggregated API design is provider-agnostic. Once the GitHub provider exists as a reference,
a `gitlab-provider` for the `api.gitlab.com` group follows the same pattern. kardinal's GitLab
SCM provider (`pkg/scm/gitlab.go`) would be replaced the same way.

---

## Pending Upstream Contributions to krocodile

These are no longer blocking kardinal — all have in-project alternatives — but worth
contributing upstream for the benefit of the broader krocodile ecosystem.

| Contribution | Kardinal alternative | Tracked |
|---|---|---|
| `recheckAfter` on Graph nodes | `ScheduleClock` CRD pattern | #134 |
| Explicit `dependsOn` edges | Positional naming workaround (acceptable) | #134 |
| `ShapeWatch` for external K8s resources | Aggregated API provider (#456) | #131 |
| CEL schedule library in DefaultEnvironment | `pkg/cel/schedule` registered via `WithCustomDeclarations` | #126 |
| `startAfterMinutes` on Graph edges | Sequential waves (deferred) | #454 |
| Aggregated API provider (GitHub) | `PRStatus` CRD workaround (#133) until landed | ellistarn/kro#80 (#456) |
