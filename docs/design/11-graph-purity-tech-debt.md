# Design: Graph Purity — Architecture Tech Debt Tracker

> Status: Active — every logic leak is tracked here
> Related: `docs/design/10-graph-first-architecture.md`
> Last audited: 2026-04-14. Status corrected on 2026-09-29: see [Current status](#current-status-2026-09-29).

---

## Agent Instructions

**Read this document at the start of every queue generation. It overrides any other scope.**

### Milestone v0.2.1: issues closed, leaks not all gone

Issues #131–#155 are closed, but some of the leaks they tracked are still in the code, and
some leaks were never catalogued. The table below is the current state.

### Current status (2026-09-29)

Checked against `main` at 3805f6a. "Accepted" means the logic stays in a reconciler that
writes its result to its own CRD status, because kro has no primitive for it (ledger
[G8](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph)).

| ID | What is still in the code | State |
|---|---|---|
| CEL-2 / PG-2 | The PolicyGate reconciler lists MetricCheck objects in Go (`buildMetricsContext`). | Still present |
| PS-4 / SCM-2 | `handleWaitingForMergeViaDirectSCM` in the PromotionStep reconciler called `GetPRStatus` when a step had no PRStatus object. | Done: removed by #1249 (3805f6a); merge state comes only from the PRStatus CRD |
| PS-6 / PS-7 | The PromotionStep reconciler still creates the auto-rollback Bundle (`createAutoRollback` in `pkg/reconciler/promotionstep/lifecycle.go`, built by `pkg/lifecycle`). | Still present |
| ST-5 / ST-6 | The `kustomize-build` step runs the `kustomize` binary (`execKustomizeBuilder` in `pkg/steps/steps/kustomize_build.go`), although #494 is closed. | Still present |
| GB-2 | Skip permissions are still checked in Go when the Graph is built (`ValidateSkipPermissions` in `pkg/graph/skip.go`). Only the gate expressions run on the Graph. | Partial |
| CLI-1 / CLI-2 / CLI-3 | The CLI no longer imports `pkg/cel`: `policy simulate` and `policy test` run the controller's PolicyGate reconciler against an in-memory client (`cmd/kardinal/cmd/policy_eval.go`). The UI validate-cel endpoint calls `policygate.ValidateExpression` (`cmd/kardinal-controller/ui_api.go`, #1248). | Done |
| PG-1 / PG-4 | The PolicyGate reconciler still requeues on a timer (`RequeueAfter: recheckInterval`, default 5m). ScheduleClock ticks add re-evaluation; they do not replace the timer. | Accepted |
| — (not catalogued) | The PolicyGate reconciler reads other CRDs in Go: ChangeWindows (`buildChangeWindowContext`), PRStatus (`buildPRContext`) and Bundles (`buildUpstreamContextWithHistory`). | Accepted |
| — (not catalogued) | The Bundle reconciler computes soak time with `time.Now` and requeues every minute while Promoting (`soakRequeue`). | Accepted |
| — (not catalogued) | The PRStatus reconciler polls the SCM API every 30 s (`requeuePollInterval`); SCM webhooks only shorten the wait. | Accepted |
| — (not catalogued) | Reconcilers make external HTTP calls: NotificationHook (webhook delivery), Subscription (registry and Git Smart HTTP reads in `pkg/source`), MetricCheck (Prometheus). Each writes the result to its own status. | Accepted |
| — (not catalogued) | The `verify-image` step runs the `cosign` binary (`pkg/steps/steps/verify_image.go`). | Accepted |

### What to work on now

Active open items are tracked in GitHub issues. Check the current open issue list.
The remaining logic leaks require either:
1. kro upstream changes (labeled `blocked-on-upstream`; gaps logged in
   [16-graph-capability-ledger.md](16-graph-capability-ledger.md)) — do not workaround
2. Large architectural work: go-git migration (#495, complete), kustomize library migration (#494, closed, but `kustomize-build` still runs the binary)

**Note:** Flat DAG compilation (#496) was evaluated and closed as architecturally unsound.
See §Flat DAG Compilation — Why It Does Not Work below.

### Hard rule: no new logic leaks

**Any PR that introduces logic outside the Graph layer (a new `time.Now()`, a new external HTTP call in a reconciler, a new cross-CRD mutation, a new CEL evaluation outside `pkg/reconciler/policygate`) requires explicit human approval before merging.**

QA must block such PRs with `[NEEDS HUMAN]`. Engineers must not implement them. This is Constitution Article XII.

---

## The Vision

In a perfectly pure architecture, kardinal-promoter is **pure YAML** from the user's perspective. No custom Go logic exists except:

1. **Owned node reconcilers** that compute a value and write it to `status.ready` — visible to the Graph
2. **CEL library extensions** on kro's Graph environment — stateless, synchronous, pure functions
3. **CLI** that reads CRDs and creates CRDs — no business logic, no API calls

Everything else is the Graph. The Graph handles sequencing, fan-out, fan-in, conditional inclusion, and teardown. All business rules are expressed as `readyWhen` / `includeWhen` / template CEL expressions on Graph nodes.

**The world is a DAG. Everything is a Graph node.**

---

## One Permitted Exception (Transitional)

The PolicyGate CEL evaluator in `pkg/reconciler/policygate` is the accepted exception. See `docs/design/10-graph-first-architecture.md` §Known Exceptions and ledger G8. It must not grow.

---

## Fixable Without Graph Changes (Milestone v0.2.1 — COMPLETE)

Issues #131–#155 are closed, but not every leak below is gone: see
[Current status](#current-status-2026-09-29). This section is preserved for historical reference.

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
| BU-1 / BU-4 | #146 | `supersedeSiblings()` in Go loop (now `isSuperseededByNewer` / `markSuperseded` in `pkg/reconciler/bundle/reconciler.go`) | Dedicated supersession reconciler watching Pipeline.status |
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
| SCM-3 / SCM-4 | #154 | `EnsureLabels` on hot path (removed: it was never called); `time.Since` in template | Setup reconciler; pre-computed CRD field |
| CLI-7 / MC-1 | #155 | PolicyGate three-way state in CLI; threshold Go enum | Add `status.phase` to PolicyGate; CEL expression field |

---

## Blocked on Upstream Graph

**Nothing in this catalog is currently blocked upstream.** Graph gaps found during the move to
upstream kro are in [16-graph-capability-ledger.md](16-graph-capability-ledger.md). All previously blocked issues have implementation
paths that require only kardinal changes. See §Previously Blocked — Now Unblocked below.

`recheckAfter` as a Graph primitive is still desirable for the general case and should
be contributed upstream — but kardinal no longer requires it as a prerequisite for any feature.

| ID | Issue | Desired Graph contribution | Priority |
|---|---|---|---|
| PG-1 / PG-4 | #138 | `recheckAfter` on Graph nodes | Nice-to-have — superseded by `ScheduleClock` pattern |
| GB-5 | #138 | Explicit `dependsOn` edges | Nice-to-have — positional workaround is correct today |
| HE-1 / HE-2 / HE-3 | #136 | Watch external K8s resources | Done with kro `ref` nodes (`pkg/health/watch_node.go`); cross-node readiness is ledger G3 |

---

## Previously Blocked — Now Unblocked

### #138 (recheckAfter): unblocked via ScheduleClock pattern

**The pre-upstream Graph controller's author suggested gating on a time-based trigger node.**

The chart creates one `ScheduleClock` object. The ScheduleClock reconciler writes
`status.tick` on a configurable interval, which fires a real Kubernetes watch event. It is
not a Graph node. The PolicyGate reconciler watches ScheduleClock objects and re-queues every
PolicyGate on each tick, so `schedule.*` expressions are re-evaluated. `schedule.*` stays a
map in the PolicyGate CEL context: kro has no way to add functions to the Graph's CEL
environment (ledger G8).

See §ScheduleClock Implementation below.

### #132 (step-as-Graph-node): **closed — not viable**

~~See §Flat DAG Compilation below.~~ This approach was evaluated and rejected.
See §Flat DAG Compilation — Why It Does Not Work below.

The observable-progress gap this issue was trying to address is solved by #592:
adding `status.steps[]` to PromotionStep (no architecture change needed).

### #130 and #68 (eliminate pkg/cel): **partially complete — schedule.* library untracked**

`pkg/cel/` no longer has an `environment.go` or a `NewCELEnvironment()` constructor — that
was deleted in #487 and the CEL environment construction moved to
`pkg/reconciler/policygate/cel_evaluator.go`. What remains in `pkg/cel/` is:
- `library/` — kardinal's copy of kro's CEL library (ALLOWED — imported by cel_evaluator.go)
- `conversion/` and `sentinels/` — utilities

The schedule.* CEL library extension (Part 2 of the ScheduleClock design) was tracked in
issue #616, which is now **closed**. The `schedule.*` functions were NOT promoted to a CEL
library during that work — they remain as plain map variables injected by the PolicyGate
reconciler. The blocking condition has changed: this is now unblocked (no Graph change
required) but untriaged. If schedule.* as a proper Graph CEL library function is still desired,
open a new issue — see #645 for context.

### #400 (Journey 2 multi-cluster): unblocked via Stage 14 implementation

Was labeled blocked via Stage 14 → #132 → the Graph controller. Since #132 is unblocked, Stage 14 is
a kardinal implementation task. Journey 2 test can be written once Stage 14 ships.

---

## ScheduleClock Implementation — #138

> **Status: Part 1 (the `ScheduleClock` CRD and reconciler) is implemented. Parts 2 and 3
> were not built and cannot be on upstream kro:** the Graph API has no way to add CEL
> functions (ledger G8), and the translator does not add a clock node to any Graph.
> `schedule.*` values are map variables in the PolicyGate reconciler. Parts 2 and 3 are
> described below as the original design.
>
> Suggested by: the pre-upstream Graph controller's author

### Problem

Time-based PolicyGate expressions (`!schedule.isWeekend`, `schedule.hour >= 9`) need
periodic re-evaluation. Nothing in the cluster changes when the clock advances — no watch
event fires, so the Graph never re-evaluates these nodes.

> **Current workaround (in production today):** The PolicyGate reconciler injects
> `schedule.*` as a plain map variable into the CEL evaluation context. The
> PolicyGate reconciler watches ScheduleClock objects and re-enqueues all PolicyGates on
> each tick, triggering re-evaluation. This works, but `schedule.*` is not available in Graph
> `readyWhen` or template expressions — only in the PolicyGate CEL context.

### Solution

**Two parts that compose:**

**1. `ScheduleClock` CRD** — a CR (created by the chart, not a Graph node) whose reconciler
writes a timestamp on a fixed interval, generating real Kubernetes watch events:

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

**2. `schedule.*` CEL library on the Graph's CEL environment** — not built. It needs a
way to register functions on kro's Graph CEL environment, which the Graph API does not
have (ledger G8). No `pkg/cel/schedule` package exists.

**3. Graph builder wires a clock dependency** into every PolicyGate node whose expression
contains `schedule.` — not built. The original design used a `propagateWhen` field, which
kro does not have.

### What Parts 2 and 3 would have eliminated

- All inline `time.Now()` / weekday/hour map computation in `policygate/reconciler.go`
- `schedule.*` scope limited to PolicyGate (once it's a Graph CEL library, it's available everywhere)

The PolicyGate `RequeueAfter` timer was not eliminated either; see
[Current status](#current-status-2026-09-29).

### One ScheduleClock per cluster is sufficient

All pipelines share one `kardinal-clock` in `kardinal-system`. The 1-minute tick interval
is appropriate for time-based gates — gates that care about business hours don't need
sub-minute precision.

---

## Flat DAG Compilation — Why It Does Not Work

> **Status: Closed / Not Viable** — Issue #496 closed 2026-04-15
> Previously this section argued for flat DAG compilation. That argument was wrong.
> This section now documents the analysis so the mistake is not repeated.

### The original claim (incorrect)

That each promotion step (git-clone, kustomize-set-image, git-commit, open-pr, wait-for-merge,
health-check) could become a separate Graph node — a `PromotionStepTask` CRD — and
the Graph controller would sequence them via `readyWhen` expressions.

### Why it does not work

Graph nodes communicate **exclusively through Kubernetes resource fields written to
etcd**. A node manages a CR, the CR signals ready via its `status`, the Graph advances.
That is the entire inter-node contract.

The step engine is a **sequential pipeline of imperative side effects with shared mutable
filesystem state**:

- `git-clone` produces a local temp directory → consumed by `kustomize-set-image`
- `kustomize-set-image` mutates files in that directory → consumed by `git-commit`
- `git-commit` produces a commit SHA → consumed by `open-pr`
- `open-pr` produces a PR URL → consumed by `wait-for-merge`

**This state cannot flow through CRD fields in etcd.** A git working directory is not a
Kubernetes resource. A commit SHA could theoretically be written to a CRD status field,
but the working directory it was committed from does not persist between reconcile loops.

Workarounds all make things worse:
- Re-clone at each step: expensive, fragile, doubles network I/O per promotion
- Shared PVC: requires a storage provisioner, adds volume lifecycle management,
  does not work with multiple controller replicas
- Encode the whole working tree in CRD fields: absurd — you would be storing git repos in etcd

**Nested Graphs do not help.** A child Graph is independently reconciled in its own
loop. You cannot pipe ephemeral local state from one reconcile loop to another. Nesting adds
scopes, not shared memory.

### What the current architecture gets right

`PromotionStep` is the correct unit of Graph-observable state. It encapsulates the full
sequential pipeline for one environment and exposes a single `status.state = Verified`.
The Graph sees that signal and advances downstream environments.

An Owned-node reconciler that internally sequences imperative operations before writing its
final status is **correct reconciler design**, not a Graph-first violation. The logic-leak
classification of `Engine.ExecuteFrom()` as a violation was wrong.

### The correct fix for the observable-progress gap

Issue #592: add `status.steps[]` to PromotionStep. No new CRD, no architecture change,
no risk. The step engine already tracks per-step execution. Writing that state to CRD
status is a small delta that gives full observability without any structural change.

---

## Aggregated API — Future Migration Path (Upstream Proposal Closed Without Merging)

> **Status: No upstream implementation.** The aggregated-API proposal (PR #80 on the pre-upstream
> Graph controller fork) was closed as a design document without code changes on 2026-04-16.
> There is no aggregated API provider in kro. See #643 for the audit finding.
>
> The PRStatus CRD workaround (#133) was implemented as planned and is in production.
> Do not block any SCM-related work on the aggregated API — it has no current timeline.

This section describes the migration path **if and when** a kro aggregated API
provider is contributed. It is a future design, not an imminent dependency.

### What the aggregated API design described

A design by the pre-upstream Graph controller's author for serving external system state as native Kubernetes
resources via the Kubernetes API Aggregation Layer. The first provider was to be GitHub (`api.github.com`),
exposing `GithubArtifact` and `GithubAuthentication` resources. Graphs would consume these through
existing `ref` semantics — no new Graph primitives required.

Key properties described:
- **No CRD sync drift** — state is served live via aggregated apiserver, not copied into CRDs
- **Request deduplication** — N Graphs watching the same GitHub path produce one upstream API call
- **ETag-based caching** — `If-None-Match` avoids counting polls against rate limits
- **Content-hash resourceVersion** — watch events fire only when data actually changes
- **OAuth device flow** — no PAT management; `kubectl get githubartifacts` shows the auth URL

### What this would eliminate in kardinal

If an aggregated API provider for GitHub were contributed to kro, kardinal could refactor:

| Logic leak | Current (purity violation) | With aggregated API (pure) |
|---|---|---|
| `GetPRStatus()` in reconciler hot path (#133) | Live GitHub API call in 5 code paths | Watch node on `GithubArtifact` for the PR branch |
| `PRStatus` CRD reconciler (#133) | kardinal-owned reconciler calls GitHub | Replaced by `GithubArtifact` Watch node |
| `git clone` in step engine (#140) | go-git clone in the step engine (the `exec.Command` was removed in #495) | Watch node on `GithubArtifact` for repo path |
| `EnsureLabels()` repo config (#149) | Removed; it was never called. GitHub creates labels on first use | — |
| `PAT-in-Secret` auth model | User manages PAT lifecycle | OAuth device flow via `GithubAuthentication` |
| Subscription CRD polling | Polling reconciler (`pkg/reconciler/subscription`) that requeues on `spec.interval` | Watch node on `GithubArtifact` where `status.sha` changes → create Bundle |

This single aggregated API adoption PR would close issues **#128, #133, #140, #143, #149**
and unblock the Subscription CRD implementation as a clean Watch node.

### Future migration path

If that proposal or an equivalent is ever contributed to [kro](https://github.com/kubernetes-sigs/kro):

1. Deploy the `github-provider` aggregated API server alongside the kardinal controller
   (ship as an optional component in the kardinal Helm chart — off by default, enabled via
   `github.provider.enabled: true`)
2. Replace `PRStatus` CRD + reconciler with a Watch node on `GithubArtifact`
3. Replace `git clone` exec.Command with Watch node on `GithubArtifact` for repo reads
4. Remove `pkg/scm/github.go` API call paths; the aggregated API server handles all GitHub I/O
5. Update `kardinal init` to guide users through OAuth device flow instead of PAT creation
6. Implement Subscription CRD as a Graph watching `GithubArtifact` for SHA changes

**Do not defer any current SCM work on expectation of this landing — it has no timeline.**

### GitLab provider

The aggregated API design is provider-agnostic. Once the GitHub provider exists as a reference,
a `gitlab-provider` for the `api.gitlab.com` group follows the same pattern. kardinal's GitLab
SCM provider (`pkg/scm/gitlab.go`) would be replaced the same way.

---

## Pending Upstream Contributions to kro

These are no longer blocking kardinal — all have in-project alternatives — but worth
contributing upstream for the benefit of the broader kro ecosystem. New Graph gaps go in
[16-graph-capability-ledger.md](16-graph-capability-ledger.md).

| Contribution | Kardinal alternative | Tracked |
|---|---|---|
| `recheckAfter` on Graph nodes | `ScheduleClock` CRD pattern | #134 |
| Explicit `dependsOn` edges | Positional naming workaround (acceptable) | #134 |
| Graph CEL extension functions (for example a `schedule` library) | `schedule.*` map in the PolicyGate context (ledger G8) | #126 |
| `startAfterMinutes` on Graph edges | Sequential waves (deferred) | #454 |
| Aggregated API provider (GitHub) | `PRStatus` CRD workaround (#133) until landed | #456 |
