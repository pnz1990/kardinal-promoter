# Design: Graph-First Architecture

> Status: Active — governs all implementation decisions
> Created: 2026-04-10
> Authors: Architecture session

---

## Decision

**Everything in kardinal-promoter is a derivation of the krocodile Graph primitive.**

The world is a DAG. Every promotion step, every policy gate, every health check,
every metric condition, every external approval — all of it is expressed as a node
in a kro Graph. This is not an aspiration. It is the governing constraint on every
implementation decision in this codebase.

If a feature cannot be expressed as a Graph node, that is a signal that either:
1. krocodile is missing a primitive that should be contributed upstream, or
2. The feature is being designed incorrectly.

In neither case does the correct response involve implementing logic outside the
Graph layer. The correct response is: **stop, escalate to human, and resolve the
gap before implementing**.

---

## The Layer Model

```
L1: krocodile Graph API
    — The universal DAG primitive
    — Creates, reconciles, and tears down Kubernetes resources in dependency order
    — Evaluates CEL expressions for readyWhen, propagateWhen, includeWhen
    — Knows only about Kubernetes resource objects — nothing else

L2: kardinal APIs (built on Graph)
    — PromotionStep CR: a Kubernetes resource representing one environment promotion
    — PolicyGate CR:    a Kubernetes resource representing a gate's evaluation result
    — Bundle CR:        the artifact being promoted
    — Pipeline CR:      the user-facing intent that the translator converts to a Graph
    — All of these are either Watch nodes or Owned nodes in the Graph
    — All logic is expressed as CEL expressions on node readyWhen/propagateWhen
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

**Q1. Can this be a Watch node?**

A Watch node (`ShapeWatch`) reads an existing Kubernetes resource into the Graph scope.
The resource's fields become available in CEL expressions. No creation, no ownership.

Examples:
- "Block if bundle author is dependabot" → Watch the Bundle CR, check
  `bundleNode.metadata.annotations['kardinal.io/author']` in `readyWhen`
- "Block if change freeze is active" → Watch a `ChangeFreeze` CR managed by an admin,
  check `freeze.status.active == false` in `readyWhen`
- "Block if manual approval is pending" → Watch an `Approval` CR, check
  `approval.status.approved == true` in `readyWhen`

**Q2. Can this be an Owned node whose status is written by a reconciler?**

A reconciler creates a CR and evaluates whatever logic is needed (including time,
external HTTP calls, complex computations). It writes the result to `status.ready`.
The Graph watches `status.ready` via `readyWhen`.

This is the correct pattern for:
- Time-based gates: the PolicyGate reconciler calls `time.Now()`, writes `status.ready`
- Metric gates: a MetricGate reconciler queries Prometheus, writes `status.ready`
- External approval: a webhook handler updates `status.approved`, Graph watches it

**Q3. Can this be expressed as a CEL extension on the Graph's CEL environment?**

Custom CEL libraries (`schedule.isWeekend()`, `quantity.parse()`) can be registered
on the Graph's CEL environment via `WithCustomDeclarations`. This is appropriate for
**stateless, cheap, synchronous** computations (time functions, string utilities,
Kubernetes quantity parsing).

This is **not** appropriate for:
- HTTP calls (blocks reconcile loop)
- External API queries (no retry, no backoff, no timeout injection)
- Non-deterministic operations

**If none of Q1-Q3 apply: STOP. This requires human architectural input.**

---

## CEL Evaluation: Where It Lives

There is exactly one place CEL expressions are evaluated for Graph-level semantics:
**inside the krocodile Graph controller**, during the DAG walk. All other CEL
evaluation in kardinal is either:

1. A **reconciler that computes a result and writes it to a CRD status** (so Graph
   can read the result via readyWhen), or
2. A **CEL library extension** registered on the Graph's environment.

There is no separate "kardinal CEL evaluator" at steady state. `pkg/cel` in its
current form exists as a transitional artifact (see Known Exceptions below) and
must be eliminated by migrating to one of the patterns above.

---

## Upstream Contribution Policy

Two capabilities are currently missing from krocodile that would eliminate
transitional workarounds:

### Contribution 1: `recheckAfter` (critical)

**Problem:** Time-based and metric-based gates need periodic re-evaluation.
Currently the PolicyGate reconciler implements this via `ctrl.Result{RequeueAfter: N}`
which triggers a watch event that causes the Graph to re-evaluate. This is a
workaround — it couples the re-evaluation cadence to the PolicyGate reconciler
rather than expressing it as a Graph primitive.

**Solution:** A native `recheckAfter` field on Graph nodes. When a node's
`readyWhen` evaluates to false, the Graph controller automatically re-queues the
node after the specified duration. No external controller needed for time-based gates.

**Upstream target:** `experimental/docs/design/` in krocodile branch.
Track as: https://github.com/ellistarn/kro/issues (open a proposal)

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

### Exception 1: `pkg/cel/` — standalone CEL evaluator

**What it is:** A standalone CEL evaluator in `pkg/cel/` used by the PolicyGate
reconciler to evaluate policy expressions (`!schedule.isWeekend`,
`bundle.provenance.author != "dependabot[bot]"`, etc.).

**Why it exists:** Two reasons:
1. krocodile's `propagateWhen` only has access to the node's own Kubernetes object
   state. It cannot evaluate `!schedule.isWeekend` because `schedule` is not a
   Kubernetes resource — it's a domain concept.
2. krocodile has no `recheckAfter` primitive. Time-based gates require periodic
   re-evaluation driven by the PolicyGate reconciler's `ctrl.Result{RequeueAfter}`.

**The path to elimination:**
1. Contribute `recheckAfter` to krocodile. Once landed, time-based gates can use
   a `schedule` CEL library extension on the Graph environment — no external
   reconciler needed.
2. For pure resource-attribute gates (bundle author, environment name checks):
   migrate these to Watch nodes reading the Bundle CR directly. `pkg/cel` is not
   needed for these — they are pure Graph CEL expressions.
3. For metric gates: design a `MetricGate` CRD and reconciler (analogous to Argo
   Rollouts `AnalysisTemplate/AnalysisRun`). The Graph watches `MetricGate.status.ready`.
   `pkg/cel` is not needed for this either.
4. Once all gate types are migrated: delete `pkg/cel`.

**Resolution target:** `recheckAfter` contribution to krocodile + migration sprint.

**Current status:** Accepted transitional workaround. Must not grow. Must not be
referenced by any new code. New gate types must use the Watch node or Owned node
pattern.

---

## Anti-Patterns — Hard Blocks

These are violations that **QA must block** and **engineers must not implement**:

| Anti-pattern | Why it's wrong | Correct approach |
|---|---|---|
| Business logic evaluated outside a Graph node or reconciler that writes to CRD status | Violates Graph-first — logic becomes invisible to the DAG | Express as readyWhen/propagateWhen on a Watch or Owned node |
| New usage of `pkg/cel` in any package other than `pkg/reconciler/policygate` | Spreads the transitional workaround | Use Graph CEL extensions or Watch nodes |
| Reconciler that makes decisions based on fields NOT in a CRD status it owns | Hidden state outside the Graph's observable layer | Write all decisions to CRD status; Graph reads status |
| CEL expression that calls external HTTP API inside a `FunctionBinding` | Blocks the Graph controller reconcile loop; no retry/backoff | Use a dedicated reconciler + CRD status pattern |
| Dependency between nodes expressed as in-memory state rather than CRD fields | Invisible to Graph, breaks restart safety | Represent dependency as Graph edge + CRD reference |
| Bypassing Graph for "simple" cases (e.g. promoting directly without creating a Graph) | Undermines the universal DAG model | Everything goes through Graph, no exceptions |

---

## Decision Log

**2026-04-10 — Initial decision.**
After thorough research into krocodile's architecture and CEL extension mechanisms,
the team concluded that the world-is-a-DAG principle is architecturally correct and
that `pkg/cel` is a known transitional workaround pending the `recheckAfter`
upstream contribution. This design doc governs all future implementation decisions.

Human approved. Agents must treat this as a hard architectural constraint.
