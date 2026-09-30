# Design: Graph-First Architecture

> Status: Active — governs all implementation decisions
> Created: 2026-04-10
> Authors: Architecture session
> Last corrected: 2026-09-29 against upstream kro v0.10.0-rc.0. The questions below use
> kro's real node kinds (`template` and `ref` nodes, `readyWhen`, `includeWhen`, `forEach`).
> Where kro cannot do something yet, the gap is in
> [16-graph-capability-ledger.md](16-graph-capability-ledger.md).

> **See also**: `docs/design/11-graph-purity-tech-debt.md` — complete catalog of every known logic leak, with GitHub issues and elimination paths. That document is the authoritative index of architecture debt. This document states the principles; that document tracks the violations.

---

## Decision

**Everything in kardinal-promoter is a derivation of the kro Graph primitive.**

The world is a DAG. Every promotion step, every policy gate, every health check,
every metric condition, every external approval — all of it is expressed as a node
in a kro Graph. This is not an aspiration. It is the governing constraint on every
implementation decision in this codebase.

If a feature cannot be expressed as a Graph node, that is a signal that either:
1. kro is missing a primitive that should be contributed upstream (log it in
   [16-graph-capability-ledger.md](16-graph-capability-ledger.md)), or
2. The feature is being designed incorrectly.

In neither case does the correct response involve implementing logic outside the
Graph layer. The correct response is: **stop, escalate to human, and resolve the
gap before implementing**.

---

## The Layer Model

```
L1: kro Graph API (kro.run/v1alpha1, GraphKind feature gate)
    — The universal DAG primitive
    — Creates, reconciles, and tears down Kubernetes resources in dependency order
    — Evaluates CEL expressions for templates, readyWhen, includeWhen, forEach
    — Knows only about Kubernetes resource objects — nothing else

L2: kardinal APIs (built on Graph)
    — PromotionStep CR: a Kubernetes resource representing one environment promotion
    — PolicyGate CR:    a Kubernetes resource representing a gate's evaluation result
    — Bundle CR:        the artifact being promoted
    — Pipeline CR:      the user-facing intent that the translator converts to a Graph
    — PromotionStep and PolicyGate are owned (`template`) nodes; health objects are `ref` nodes
    — All logic is expressed as CEL expressions on node templates, readyWhen and includeWhen
    — OR delegated to a reconciler that writes to the CR's status (and Graph watches)

L3: kardinal customer APIs
    — Pipeline and PolicyGate definitions
    — The translator generates a Graph from their intent
```

The critical invariant:

> **No business logic lives outside the Graph layer at steady state.
> Kubernetes CRDs are the only medium through which logic results are communicated.
> Graph reads CRD status. Graph never executes business logic directly.**

---

## What "Graph-First" Means in Practice

### For every new feature, ask these questions in order:

**Q1. Can this be a `ref` node?**

A `ref` node reads an existing Kubernetes resource into the Graph scope. kro does not
create or own it. Its fields become available to the CEL expressions of nodes that
depend on it.

Two kro limits shape how a `ref` node can block a promotion:

- `readyWhen` may only reference the node itself (ledger
  [G3](16-graph-capability-ledger.md#g3-readywhen-may-only-reference-the-node-itself)).
- `readyWhen` does not hold back dependents in a standalone Graph (ledger
  [G1](16-graph-capability-ledger.md#g1-readywhen-does-not-gate-dependents-in-a-standalone-graph)).
  kardinal instead puts the condition inside a value the dependent needs
  (`resolvableWhen` in `pkg/graph/builder.go`), so the dependent stays unresolved until
  the condition holds.

Example: the health checks. `pkg/health/watch_node.go` adds a `ref` node for the
Deployment, Argo CD Application or Flux Kustomization, with a self-only `readyWhen`.

**Q2. Can this be an Owned node whose status is written by a reconciler?**

A reconciler creates a CR and evaluates whatever logic is needed (including time,
external HTTP calls, complex computations). It writes the result to `status.ready`.
The Graph watches `status.ready` via `readyWhen`.

This is the correct pattern for:
- Time-based gates: the PolicyGate reconciler calls `time.Now()`, writes `status.ready`
- Metric gates: the MetricCheck reconciler queries Prometheus and writes its result to
  `MetricCheck` status; PolicyGate expressions read it as `metrics.<name>`

**Q3. Can this be expressed as a CEL extension on the Graph's CEL environment?**

**Not available to kardinal today.** kro's Graph controller builds its own CEL
environment, and the Graph API has no way to register extra functions.
(`WithCustomDeclarations` in kro's `pkg/cel/environment.go` is an internal Go option with
test-only callers.) The gap is ledger
[G8](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph). Until kro exposes
Graph CEL extensions, use Q2. If it does, this path fits **stateless, cheap,
synchronous** computations.

> **Current status of `schedule.*`:** `schedule.isWeekend`, `schedule.hour`,
> `schedule.dayOfWeek` are **NOT yet implemented as CEL library extensions**.
> They are plain map variables injected into the PolicyGate CEL context by the
> PolicyGate reconciler. They are available in PolicyGate expressions only — they
> do NOT work in kro Graph `readyWhen` or template expressions.
> Making them a Graph CEL library depends on G8 above.

This is **not** appropriate for:
- HTTP calls (blocks reconcile loop)
- External API queries (no retry, no backoff, no timeout injection)
- Non-deterministic operations

**If none of Q1-Q3 apply: STOP. This requires human architectural input.**

---

## CEL Evaluation: Where It Lives

There is exactly one place CEL expressions are evaluated for Graph-level semantics:
**inside the kro Graph controller**, during the DAG walk. All other CEL
evaluation in kardinal is either:

1. A **reconciler that computes a result and writes it to a CRD status** (so Graph
   can read the result via readyWhen), or
2. A **CEL library extension** registered on the Graph's environment.

> **Note on `pkg/cel/`:** There is no `pkg/cel/NewCELEnvironment()` function.
> The CEL environment for PolicyGate evaluation is constructed in
> `pkg/reconciler/policygate/cel_evaluator.go:newEvaluator()` using `cel.NewEnv()`
> directly, importing kardinal's own `pkg/cel/library` (a copy adapted from kro's
> `pkg/cel/library`; kardinal does not import kro's module for CEL).
> `pkg/cel/` contains only the library sub-package, conversion utilities, and
> sentinels — it is not a facade with a constructor. Any new feature that needs CEL
> evaluation must call `cel.NewEnv(...)` directly with explicit library imports, as
> done in `cel_evaluator.go`. The `pkg/cel/` directory must not grow.

The PolicyGate evaluator is the one accepted exception: it is a reconciler that
writes its result to PolicyGate status (pattern 1). See Known Exceptions below.

---

## Upstream Contribution Policy

The Graph gaps that force kardinal workarounds are G1-G8 in
[16-graph-capability-ledger.md](16-graph-capability-ledger.md), each with its proposed
upstream contribution. The two items below are from the original design; the
`recheckAfter` one was resolved without kro.

### ~~Contribution 1: `recheckAfter`~~ — RESOLVED via ScheduleClock pattern (#641)

> **Status: Superseded.** The `ScheduleClock` CRD (PR #484) provides watch-driven
> re-evaluation without a Graph-native `recheckAfter` primitive. This contribution
> is now **nice-to-have** for the broader kro ecosystem, not a kardinal prerequisite.
> See `docs/design/11-graph-purity-tech-debt.md §ScheduleClock Implementation` and
> §Pending Upstream Contributions for context.

~~**Problem:** Time-based and metric-based gates need periodic re-evaluation.
Currently the PolicyGate reconciler implements this via `ctrl.Result{RequeueAfter: N}`...~~

The kardinal solution: the chart creates a `ScheduleClock` object, and the ScheduleClock
reconciler writes `status.tick` on a configurable interval, generating real Kubernetes
watch events. It is not a Graph node. The PolicyGate reconciler watches ScheduleClock
objects and re-queues every PolicyGate on each tick
(`pkg/reconciler/policygate/reconciler.go` `SetupWithManager`) — no `recheckAfter`
primitive required.

If contributing upstream is desired, the target is
[kubernetes-sigs/kro](https://github.com/kubernetes-sigs/kro). It is not blocking any kardinal work.

### Contribution 2: `dependsOn` (explicit edges)

**Problem:** Some dependency edges are not expressible via data-flow alone.
Currently expressed by referencing upstream node fields in templates.

**Solution:** An explicit `dependsOn: [nodeID]` field on Graph nodes for
structural dependencies that don't involve data flow.

**Upstream target:** Same as above.

---

## Known Exceptions (Transitional — Must Be Resolved)

These are known violations of the Graph-first principle that exist as intentional
transitional workarounds. Each must be tracked to resolution. **No new exceptions
are permitted without explicit human approval.**

### Exception 1: ~~`pkg/cel/` — standalone CEL evaluator~~ RESOLVED (#130)

**Status: RESOLVED** — PR #487. The `pkg/cel/evaluator.go` and `pkg/cel/environment.go`
files have been deleted. The CEL evaluator is now inline in `pkg/reconciler/policygate/`
as a package-private implementation detail. `pkg/cel` now contains only the kro library
extensions (library/, conversion/, sentinels/) which are explicitly allowed.

The ScheduleClock CRD (PR #484) provides watch-driven re-evaluation, eliminating the
need for a separate recheckAfter primitive.

**Historical note:**

**What it was:** A standalone CEL evaluator in `pkg/cel/` used by the PolicyGate
reconciler to evaluate policy expressions.

**Why it existed:**
1. The Graph controller's node conditions only have access to the node's own Kubernetes object state.
2. The Graph controller had no `recheckAfter` primitive (now replaced by ScheduleClock pattern).

**Resolution path taken:**
1. ScheduleClock CRD provides watch-driven re-evaluation (PR #484 — closes the recheckAfter gap).
2. CEL evaluator moved inline to `pkg/reconciler/policygate/cel_evaluator.go` (PR #487).
3. `pkg/cel/environment.go` and `pkg/cel/evaluator.go` deleted.

**Remaining work**: None. pkg/cel is now library-only. The library package imports are allowed.

**Current status:** The PolicyGate CEL evaluator in `pkg/reconciler/policygate` is an
accepted exception (ledger G8). It must not grow. New gate types must use the `ref`
node or owned node pattern.

---

## Anti-Patterns — Hard Blocks

These are violations that **QA must block** and **engineers must not implement**:

| Anti-pattern | Why it's wrong | Correct approach |
|---|---|---|
| Business logic evaluated outside a Graph node or reconciler that writes to CRD status | Violates Graph-first — logic becomes invisible to the DAG | Express as readyWhen on a `ref` node, or resolvability gating on an owned (`template`) node |
| New usage of `pkg/cel` in any package other than `pkg/reconciler/policygate` | Spreads the exception | Use a reconciler that writes CRD status, or a `ref` node |
| Reconciler that makes decisions based on fields NOT in a CRD status it owns | Hidden state outside the Graph's observable layer | Write all decisions to CRD status; Graph reads status |
| CEL expression that calls external HTTP API inside a `FunctionBinding` | Blocks the Graph controller reconcile loop; no retry/backoff | Use a dedicated reconciler + CRD status pattern |
| Dependency between nodes expressed as in-memory state rather than CRD fields | Invisible to Graph, breaks restart safety | Represent dependency as Graph edge + CRD reference |
| Bypassing Graph for "simple" cases (e.g. promoting directly without creating a Graph) | Undermines the universal DAG model | Everything goes through Graph, no exceptions |

---

## Decision Log

**2026-04-10 — Initial decision.**
After thorough research into the Graph controller's architecture and CEL extension mechanisms,
the team concluded that the world-is-a-DAG principle is architecturally correct and
that `pkg/cel` is a known transitional workaround pending the `recheckAfter`
upstream contribution. This design doc governs all future implementation decisions.

Human approved. Agents must treat this as a hard architectural constraint.

**2026-09 — Upstream kro.** kardinal moved from the pre-upstream Graph controller fork to
upstream kro v0.10.0-rc.0. The principle is unchanged; gaps are tracked in
[16-graph-capability-ledger.md](16-graph-capability-ledger.md).
