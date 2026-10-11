# Design: Graph-First Architecture

> Status: Active — governs all implementation decisions
> Created: 2026-04-10
> Authors: Architecture session
> Last corrected: 2026-09-29 against upstream kro v0.10.0-rc.0. The questions below use
> kro's real node kinds (`template` and `ref` nodes, `readyWhen`, `includeWhen`, `forEach`).
> Where kro cannot do something yet, the gap is in
> [16-graph-capability-ledger.md](16-graph-capability-ledger.md).

> **See also**: ledger [G8](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph) lists every piece of logic still outside the Graph. This document states the principles; the ledger tracks the exceptions.

---

## Decision

**Everything in kardinal-promoter is a derivation of the kro Graph primitive.**

The world is a DAG. Every promotion step, every policy gate, every health check,
every metric condition, every external approval — all of it is expressed as a node
in a kro Graph. This is not an aspiration. It is the governing constraint on every
implementation decision in this codebase.

Today some of this logic still runs in reconcilers that write their own CRD status.
Ledger [G8](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph) lists each piece.

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
Planned: #1283 (open) drops these ref nodes. The PromotionStep's Go adapters already
decide health; the ref nodes only feed Graph readiness.

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
> `pkg/cel/` contains only the library sub-package and conversion utilities —
> it is not a facade with a constructor. Any new feature that needs CEL
> evaluation must call `cel.NewEnv(...)` directly with explicit library imports, as
> done in `cel_evaluator.go`. The `pkg/cel/` directory must not grow.

The PolicyGate evaluator is the one accepted exception: it is a reconciler that
writes its result to PolicyGate status (pattern 1). See Known Exceptions below.

---

## Upstream Contribution Policy

The Graph gaps that force kardinal workarounds are in
[16-graph-capability-ledger.md](16-graph-capability-ledger.md), each with its proposed
upstream contribution. Re-evaluation of time-based gates needs no kro primitive: the chart
creates a `ScheduleClock` object, its reconciler writes `status.tick` on an interval, and the
PolicyGate reconciler re-queues every PolicyGate on each tick
(`pkg/reconciler/policygate/reconciler.go` `SetupWithManager`).

### `dependsOn` (explicit edges)

**Problem:** Some dependency edges are not expressible via data-flow alone.
Currently expressed by referencing upstream node fields in templates.

**Solution:** An explicit `dependsOn: [nodeID]` field on Graph nodes for
structural dependencies that don't involve data flow.

**Upstream target:** Same as above.

---

## Known Exceptions

The accepted exceptions are listed in ledger
[G8](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph). **No new exceptions are
permitted without explicit human approval.**

The PolicyGate CEL evaluator in `pkg/reconciler/policygate` is one of them: it evaluates
`spec.expression` with kardinal's `pkg/cel/library` and writes the result to PolicyGate status.
It must not grow. New gate types must use the `ref` node or owned node pattern.

---

## Anti-Patterns — Hard Blocks

These are violations that **review must block** and **engineers must not implement**:

| Anti-pattern | Why it's wrong | Correct approach |
|---|---|---|
| Business logic evaluated outside a Graph node or reconciler that writes to CRD status | Violates Graph-first — logic becomes invisible to the DAG | Express as readyWhen on a `ref` node, or resolvability gating on an owned (`template`) node |
| New usage of `pkg/cel` in any package other than `pkg/reconciler/policygate` | Spreads the exception | Use a reconciler that writes CRD status, or a `ref` node |
| Reconciler that makes decisions based on fields NOT in a CRD status it owns | Hidden state outside the Graph's observable layer | Write all decisions to CRD status; Graph reads status |
| CEL expression that calls external HTTP API inside a `FunctionBinding` | Blocks the Graph controller reconcile loop; no retry/backoff | Use a dedicated reconciler + CRD status pattern |
| Dependency between nodes expressed as in-memory state rather than CRD fields | Invisible to Graph, breaks restart safety | Represent dependency as Graph edge + CRD reference |
| Bypassing Graph for "simple" cases (e.g. promoting directly without creating a Graph) | Undermines the universal DAG model | Everything goes through Graph, no exceptions |
