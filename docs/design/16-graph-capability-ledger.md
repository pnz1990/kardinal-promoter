# Graph Capability Ledger

> Status: Active. This is the single list of places where the kro Graph does not do what kardinal needs.
> Target: kro `kro.run/v1alpha1` Graph, [v0.10.0-rc.0](https://github.com/kubernetes-sigs/kro/releases/tag/v0.10.0-rc.0) (`54a203b`), `GraphKind` feature gate on.
> Docs: <https://kro.run/next/docs/concepts/graph/overview/>
> Last verified: 2026-09-29, kind e2e (kind v1.33, kro v0.10.0-rc.0, Argo CD v2.10)
> Upstream survey: 2026-10-02, kro `main` at `e1b94df` (18 commits past the pin, no Graph-path
> behavior change). See [Upstream survey](#upstream-survey-2026-10-02).
>
> **Planned workarounds.** Text marked **Planned in #N (not on main)** describes a workaround
> in an open pull request #N. That code is not on main yet; check the PR before you rely on
> it. The PR that merges the workaround removes the mark. (Checked against main on 2026-10-09.)

---

## How to use this document

The goal is for kardinal to be built entirely on the Graph. Every piece of promotion
logic that lives outside the Graph, and every workaround kardinal applies to make the
Graph behave, is logged here with:

- **Need**: what kardinal needs from the Graph.
- **kro today**: what v0.10.0-rc.0 actually does, with source evidence (`path:line` in
  `kubernetes-sigs/kro` at the tag above).
- **kardinal workaround**: what we do instead, and where.
- **Upstream work**: kro KREPs, issues and PRs that touch the gap, with how much of it they
  would cover (key below), and the smallest kro change that would let us delete the workaround.

Coverage key, from kardinal's point of view:

- **Complete**: if it lands (or has landed) as written, kardinal can delete the workaround.
- **Partial**: closes part of the gap; the entry says which part is left.
- **None**: related, but does not help. Listed so nobody has to check it again.

kro and kardinal issue numbers overlap. kro items are always written as links
(`kro#861`); a bare `#1283` is a kardinal issue.

Rules:

1. Before adding logic outside the Graph, check this list. If the gap is new, add an entry
   first (`docs/design/10-graph-first-architecture.md`).
2. When kro ships a fix, verify it on kind, delete the workaround, and move the entry to
   **Closed** with the kro version.
3. GitHub issues blocked on one of these entries carry the `blocked-on-upstream` label.
   kardinal has not filed or commented on any kro issue for these gaps yet, and no kardinal
   issue carries `blocked-on-upstream`. The issues and comments we plan to post are listed in
   [Engagement](#engagement).
4. The public summary of this ledger is [Graph Coverage](../graph-coverage.md). When an
   entry opens, changes or closes, update that page in the same PR.

## Summary

| ID | Gap | Severity | Workaround lives in | Upstream work (2026-10-02) |
|----|-----|----------|---------------------|----------------------------|
| [G1](#g1-readywhen-does-not-gate-dependents-in-a-standalone-graph) | `readyWhen` does not gate dependents in a standalone Graph | High | `pkg/graph/builder.go` `resolvableWhen` | Partial and stalled (KREP-006); nothing filed for the smallest fix |
| [G2](#g2-includewhen-is-contagious) | `includeWhen` is contagious to every dependent | Medium | `pkg/graph/builder.go` static `skipEnvironments` filter | Partial and rotten (KREP-018) |
| [G3](#g3-readywhen-may-only-reference-the-node-itself) | `readyWhen` may only reference the node itself | Low (reclassified 2026-10-02) | health stays in the PromotionStep reconciler | None, and not needed |
| [G4](#g4-a-missing-crd-fails-the-whole-graph) | A missing CRD fails the whole Graph compile | Low (solved in kro, not adopted) | `pkg/translator/translator.go` `servedKind` filter | Complete for the compile failure (dynamic types, in the pin) |
| [G5](#g5-the-graph-identity-is-provisioned-outside-the-graph) | The Graph's ServiceAccount and RBAC live outside the Graph | Medium | `pkg/graph/identity.go` `IdentityProvisioner` | Partial (kro#1402 docs, kro#1465) |
| [G6](#g6-spec-changes-are-handled-by-delete-and-recreate) | Spec changes handled by delete-and-recreate (kardinal bug, fixed) | Closed | `pkg/graph/client.go` in-place update | Not needed |
| [G7](#g7-no-ownerreferences-on-graph-children) | No ownerReferences on Graph children | Low | labels + kro inventory; Bundle owns the Graph only | None for the Graph kind |
| [G8](#g8-logic-still-outside-the-graph) | Logic still outside the Graph (time, CEL gates, git/SCM) | High | reconcilers; see `11-graph-purity-tech-debt.md` | Time: Partial (KREP-025, in review). Everything else: none |
| [G9](#g9-a-graph-reconcile-costs-three-api-calls-per-object) | A Graph reconcile costs about three uncached API calls per object, and kro reconciles one Graph at a time by default | High at scale | `hack/install-kro.sh` raises the worker count and client QPS; smaller Graphs | None filed; kro#1324 is related |
| [G10](#g10-a-graph-is-one-etcd-object) | A Graph's spec and inventory share one etcd object (1.5 MiB) | Medium | `pkg/graph/size.go` `CheckSize` refuses a Graph over 1.2 MB | None filed |
| [G11](#g11-collections-are-all-or-nothing) | A `forEach` collection is all-or-nothing on pending data and on apply errors, and every growth relabels every item | Medium | pacing by choosing the list; label-only events ignored | None filed |
| [G12](#g12-delete-and-prune-orphan-the-pods-of-a-job) | kro deletes and prunes without a propagation policy, so a Job node orphans its Pods, and a deleted Job runs again | Medium | Hooks use a `HookRun` CRD that owns its Job (#1443) | None filed |
| [G13](#g13-the-graph-controller-cannot-be-sharded) | kro's Graph controller is one leader with one queue | Medium | kardinal shards only its own controllers, by namespace (`--namespace-shard`, `pkg/shard`, #1462) | None filed |
| [G14](#g14-a-node-with-one-pending-field-is-wholly-unresolved) | One pending field leaves the whole node Unresolved, so live fields cannot sit next to gating fields | Medium | Mirror `patch` nodes with a literal target name (hooks, #1443; gate commit statuses **planned in #1518 (not on main)**) | None filed |
| [G15](#g15-kro-holds-every-graph-in-memory-and-a-graph-cannot-be-retired-without-its-children) | kro holds every live Graph in memory (3 to 6 MB each, whatever its size), and a Graph cannot be deleted without deleting its children | High at scale | `pkg/reconciler/bundle/retire.go` records the steps in `Bundle.status.retiredSteps`, then deletes the Graph | None filed (drafts: Graph suspend, kro#1445 comment) |

Smaller constraints that shape the translator are in [Notes](#notes-constraints-we-design-around).

---

## G1: `readyWhen` does not gate dependents in a standalone Graph

**Need.** A PromotionStep for `uat` must not be created until the `test` PromotionStep is
Verified, and a `prod` step must not exist until every PolicyGate for `prod` is ready.
That is the whole promotion DAG.

**kro today.** The executor has the switch, but it is off for the standalone Graph controller.

- `pkg/graphengine/executor/simple.go:65-69`: `GateReadiness` "withholds inclusion
  evaluation and apply until every hard dependency is ready"; `simple.go:233` says it is
  opt-in, and `simple.go:267` is the only place it is read.
- It is set only for RGD instances: `pkg/controller/instance/controller.go:172`
  (`exec.GateReadiness = true`).
- The Graph controller builds its executor with `exec.ConflictDetection = true` only
  (`cmd/controller/graphengine.go:77`). There is no Graph field to turn it on.
- Covered by `pkg/graphengine/executor/gating_test.go:79`.

Without gating, kro creates every node as soon as its CEL references resolve, so every
environment would promote at once.

**kardinal workaround.** Put the gate condition inside a value the dependent needs, so
the value only resolves when the condition holds:

```
resolvableWhen(cond, value) = ${[value].filter(x_, cond)[0]}
```

When `cond` is false the list is empty and `[0]` fails with `index out of bounds`. kro
classifies that as data-pending (`pkg/graphengine/runtime/errors.go:43-49`): the node stays
Unresolved, nothing is created or pruned, and kro retries on the next watch event.
`spec.upstreamStates` and `spec.requiredGates` of each PromotionStep are built this way
(`pkg/graph/builder.go` `resolvableWhen`, `verifiedCond`, `buildPromotionStepNode`).
So is `spec.bundleName`, on `bundle.status.phase != "Superseded" && bundle.status.phase !=
"Rejected"` and no Bundle condition `WaitingForSlot=True`, which stops the Graph of a Superseded
Bundle (E2E-R20), of a rejected one (`kardinal reject`, #1451), or of a Failed Bundle waiting for
a `maxConcurrentPromotions` slot (#1349), from creating steps.
The condition test is guarded with `has(bundle.status.conditions)`, since a missing key
would be data-pending too. A node that already exists and
turns Unresolved is neither re-applied nor pruned (`executor/simple.go:318-324`,
`pkg/controller/graph/tracking.go:102-106`), so its object stays as history.

Per-promotion MetricCheck instances (`buildMetricCheckNode`, #1445) use the same hold on
their `spec.query` (`spec.web.url` for provider `web`): kro creates the instance only once
every upstream step of the gated environment is Verified. Their `spec.suspend` is a live
`${!(bundle.status.phase in ["Available", "Promoting"])}`: kro re-applies the template when
the Bundle ref changes, so the instance stops querying once the Bundle is Verified, Failed or
Superseded, without kardinal deleting it. The placeholder values (`{{ bundle.version }}`) are
rendered by the translator, not by kro string templates: they come from the Bundle the
translator is building for, any text kro would read as `${...}` is passed as a CEL string
literal (`${"..."}`), and values outside `[A-Za-z0-9._:@+-]` are not substituted (fail
closed). Rendering them with kro CEL from the `bundle` ref would need the same escaping plus
`has()` chains for optional fields, and gives nothing the translator does not have.

Verified on kind: `uat` was created only after `test` was Verified; `prod` stayed absent
while `require-uat-soak` was not ready.

Downsides: the Graph expresses ordering through an error path; `kubectl get graph` shows
nodes as Unresolved instead of "waiting on X"; a CEL typo that also produces
`index out of bounds` would look like "not ready yet" instead of failing.

**Upstream work.** Ungated Graph readiness is a design decision, not a bug, so a fix needs
an opt-in or a KREP amendment.

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| [KREP-024 Graph](https://github.com/kubernetes-sigs/kro/pull/1302) (`docs/design/proposals/graph.md`) | Merged 2026-07-22, implemented | Defines the gap | `graph.md:322`: "Nodes do not wait for upstream dependencies to pass `readyWhen`". The user docs say the same (`concepts/graph/03-modifiers.md` "readyWhen Does Not Gate Dependents"). |
| [kro#1426](https://github.com/kubernetes-sigs/kro/pull/1426) | Merged 2026-09-17, in the pin | Complete if exposed to Graph, but it is not | Runs `GateReadiness` before `includeWhen` for RGD instances. Its description says "Standalone Graph keeps its ungated behavior." The engine already does what G1 needs. |
| [KREP-006 Propagation Control](https://github.com/kubernetes-sigs/kro/pull/861) (`propagateWhen`, `.ready()`) | Open, not approved, no activity since 2026-05-13, `cncf-cla: no` | Partial | `propagateWhen: ["${uat.ready()}"]` on `prod` is per-edge readiness gating. But the KREP is written for RGD, still assumes dependents wait (its lines 146-148, which KREP-024 removed for Graph), and nothing is implemented. KREP-024 lists it as "Freeze: last-applied state persists" (`graph.md:263`). Complete only if it lands for Graph with "Pending" semantics: the node keeps its object, is not pruned, and its dependents wait. |
| [krocodile](https://github.com/ellistarn/kro/blob/krocodile/experimental/docs/design/001-graph.md) | Prototype in a personal fork, cited by KREP-024 | Complete in the prototype | `propagateWhen` with `.ready()`. An unsatisfied gate leaves the node Pending, Pending spreads to dependents, and any Pending node blocks prune. These are the semantics to ask KREP-006 for. |
| [kro#110 dependsOn](https://github.com/kubernetes-sigs/kro/issues/110) | Open, frozen, needs a KREP | None | An explicit edge on a Graph only orders evaluation. `prod` would still be applied once `uat` is applied, not Verified. |
| [KREP-019 `defer()`](https://github.com/kubernetes-sigs/kro/pull/1126) | Open, rotten | None | The inverse of G1: lets a node render before its dependency has data. |

**Smallest change, no upstream work yet.** Let a Graph opt into the existing gate:
`spec.gateReadiness: true` (or an annotation) makes `executorFor`
(`pkg/controller/graph/impersonation.go:83`) return a copy of the `Simple` executor with
`GateReadiness = true`. No engine change; tests can reuse `executor/gating_test.go`. We plan
to open this as a kro issue and offer the PR ([Engagement](#engagement)).

**If it lands.** `spec.upstreamStates` and `spec.requiredGates` become plain references
(`${uat.metadata.name}`, `${<gate>.metadata.name}`), and `resolvableWhen`, `verifiedCond`
and the error-text dependency go away. The Superseded hold on `spec.bundleName` would move
to a `readyWhen` on the `bundle` ref, which leaves a superseded Graph `Ready=False` for good;
keeping `resolvableWhen` for that one field avoids it. `checkRequiredGates` (gate freshness
against the step's creation time) and `holdIfPaused` stay: they are separate invariants.

---

## G2: `includeWhen` is contagious

**Need.** Skip an environment for one Bundle (for example `skipEnvironments: [uat]` or an
environment that does not apply to a config-only Bundle) while downstream environments
still run, wired to the skipped environment's upstream.

**kro today.** An excluded node excludes everything that depends on it.
`pkg/graphengine/runtime/node.go:127-150` (`IsIgnored`: "any of its dependencies is
ignored (contagious propagation)"), applied by the executor's `IsIgnored` call at `simple.go:275` and the skip block at
`simple.go:289-296`.

**kardinal workaround.** Skipping is resolved statically when the Graph is built. The builder
drops skipped environments and rewires their dependents to the nearest non-skipped upstream
(`pkg/graph/builder.go` `filterByIntent`; `builder_test.go` and `skip_test.go` cover it). A skip
decision that depends on runtime state cannot be expressed. When a skip needs a skip-permission
gate, only its existence is checked at build time (`pkg/graph/skip.go` `ValidateSkipPermissions`);
its expression runs on the Graph, as a PolicyGate instance in front of each kept environment that
depended on the skipped one (`skipPermissionGates`).

**Upstream work.**

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| [KREP-018 partial dependencies in branching expressions](https://github.com/kubernetes-sigs/kro/pull/1125) | Open, approved by its author, rotten since 2026-07-29 | Partial | The compiler waits only on the dependencies of the ternary branch that is selected, so `prod` could use `${skipUat ? <test value> : <uat value>}` and not be excluded with `uat`. Left over: the skip predicate is repeated in every dependent; an already-created `uat` step is still pruned when the skip flips; and on an ungated Graph a predicate that reads an interim value prunes, which is why `GateReadiness` exists (`executor/simple.go:226-233`). The KREP does not say whether it applies to Graph; KREP-024 says every reference is hard (`graph.md:509`). |
| [KREP-008 includeWhen references](https://github.com/kubernetes-sigs/kro/issues/845) (`include-when-resource-references.md`) | Merged and implemented | None | It is the mechanism that causes G2: excluding X excludes and prunes everything that references X. |
| [krocodile](https://github.com/ellistarn/kro/blob/krocodile/experimental/docs/design/001-graph.md) | Personal fork | Partial | References made only through `?.` are soft and not contagious. But "excluded" and "not created yet" look the same to the dependent, so the predicate must still be repeated. |
| [KREP-017 `omit()`](https://github.com/kubernetes-sigs/kro/issues/928) | Implemented behind `CELOmitFunction` | None | Drops a field, not a node; cannot rewire a dependent. |
| [KREP-014 lifecycles](https://github.com/kubernetes-sigs/kro/pull/1091), [kro#1264](https://github.com/kubernetes-sigs/kro/pull/1264) | Stale; draft on the pre-`graphengine` code; RGD only | None | Retain-on-prune orphans the object, and dependents are still excluded. |
| [KREP-019 `defer()`](https://github.com/kubernetes-sigs/kro/pull/1126) | Rotten | None | Hard edges still propagate exclusion. |

**Smallest change, no upstream work yet.** A built-in `<id>.excluded()` that is true only when
the node was skipped by its `includeWhen`, with kro treating references reached only through
the non-excluded branch as non-contagious (`${uat.excluded() ? test.status.state :
uat.status.state}`). That is KREP-018 restricted to one predicate. It is only safe together
with G1, so that an `includeWhen` that reads an interim value does not prune.

**Ask.** None for now. kardinal builds one Graph per Bundle, so the static `filterByIntent`
covers every skip the product ships. An optional data-point comment on KREP-018 is drafted.

---

## G3: `readyWhen` may only reference the node itself

**Need.** "prod is healthy" means "the Argo CD Application is Healthy and Synced **at the
commit this PromotionStep pushed**". That is a cross-node condition: the health object
and the PromotionStep's `status.outputs`.

**kro today.** The compiler rejects any other node in `readyWhen`:
`pkg/graphengine/compiler/compiler.go:749-766` (`analyzeReadyWhen`: "may only reference
the node itself").

**kardinal workaround.**

- Health checks are separate ref nodes (`healthProd` and similar) with a self-only
  `readyWhen` (`pkg/health/watch_node.go`, `pkg/translator/translator.go`
  `healthInjector.inject`). They feed Graph readiness, not the PromotionStep's own `readyWhen`.
- The PromotionStep reconciler still runs the Go health adapter to move the step from
  HealthChecking to Verified (`pkg/health/adapter.go`).
- **Solved in the reconciler (2026-09 audit, E2E-01):** the PromotionStep reconciler's
  Go adapter now checks the revision. It records the pushed commit
  (`status.outputs.commitSHA`) or the merge commit (`status.outputs.mergeCommitSHA`, or
  the PRStatus `status.mergeCommitSHA`) and requires Argo CD's synced revision or Flux's
  `lastAppliedRevision` to match it, and a Deployment to run the Bundle images
  (`pkg/health/adapter.go`, `pkg/reconciler/promotionstep/reconciler.go` `expectedRevision`).
- **Still not solved in the Graph:** the ref health nodes keep a self-only `readyWhen`
  (for Argo CD: `Healthy`, `Synced` and no running or failed sync operation), so Graph
  readiness alone can see the previous revision.
  Doing it in the Graph needs `app.status.sync.revision == step.status.outputs.commitSHA`,
  which is a cross-node `readyWhen`.
- **Planned:** #1283 (open) removes the health ref nodes and the reader RBAC (ClusterRole,
  RoleBindings, `Prune`, `--graph-reader-namespaces`). Health stays in the PromotionStep
  reconciler.

**Reclassified to Low (2026-10-02).** A cross-node `readyWhen` would only change Graph
readiness: on a standalone Graph `readyWhen` does not gate dependents (G1), and the Graph
cannot write PromotionStep status, so `HealthChecking → Verified` stays in the reconciler
either way. Graph `Ready` already includes the revision check, because the step node's
`readyWhen` is `state == "Verified"` and the reconciler sets Verified only after the
revision-checked adapter passes. G3 closes with #1283.

**Upstream work.**

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| `def` node with a cross-node value (in the pin) | Supported (`concepts/graph/03-modifiers.md`; `compiler_test.go` "readyWhen self-reference is filtered out") | Partial | A `def` node `{healthy: ${app.status.sync.revision == step.status.outputs.commitSHA}}` with `readyWhen: ${check.healthy}` passes the self-only rule and feeds Graph `Ready`. It still does not gate dependents or set the step's state, and it needs the health read access #1283 removes. |
| [kro#1337](https://github.com/kubernetes-sigs/kro/pull/1337) readyWhen referencing other resources | Open, stale, needs rebase | None | RGD instance controller only. The Graph rule is in `graphengine/compiler.analyzeReadyWhen`, which it does not touch. |
| `docs/design/proposals/status-conditions.md` (custom status conditions) | Merged design, not implemented for Graph | None | Writes RGD instance status. A Graph has no user-defined status. |

**Smallest change, no upstream work yet.** In `analyzeReadyWhen`, accept references to nodes
already in the node's hard-dependency set (no new edges). Not worth asking for: it would
add nothing kardinal uses.

---

## G4: A missing CRD fails the whole Graph

**Need.** A Pipeline may name a health type (Flux, Argo Rollouts, Flagger) whose CRD is
not installed yet. The rest of the promotion should still run.

**kro today.** A literal GVK is resolved at compile time. A kind the API server does not
serve fails the whole Graph: `pkg/graphengine/compiler/context.go:265-280`
(`ResolveSchema` and `RESTMapping` errors are returned). **Correction (2026-10-02):** a GVK
with `${}` in `apiVersion` or `kind` is a dynamic type (`context.go:242`, `isDynamicGVK`).
Its schema and REST mapping are skipped at compile time and resolved when the node runs. If
the kind is not served, the node stays Unresolved with a soft not-ready, and the
SchemaWatcher re-enqueues the Graph when a CRD appears
(`concepts/graph/02-nodes.md` §Dynamic types, `executor/dynamic_ref_test.go`).

**kardinal workaround.** The translator asks the RESTMapper whether the kind is served and
drops the health ref node if it is not (`pkg/translator/translator.go` `servedKind`,
`WithRESTMapper`). The PromotionStep's Go health adapter still runs, so the step stays in
HealthChecking with a "not found" reason until the kind and object exist.

**Planned:** #1283 (open) removes the health ref nodes and the reader RBAC (ClusterRole,
RoleBindings, `Prune`, `--graph-reader-namespaces`). Health stays in the PromotionStep
reconciler.

**Upstream work.**

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| Graph dynamic types (in the pin) | Shipped and documented; maintainers point users to it on [kro#1293](https://github.com/kubernetes-sigs/kro/issues/1293) and [kro#1243](https://github.com/kubernetes-sigs/kro/issues/1243), and a user confirmed it on rc.0 | Complete for the compile failure; Partial for the need | A health ref with `apiVersion: ${"argoproj.io/v1alpha1"}` would no longer fail the Graph. Left over: the node is Unresolved, not excluded, so Graph `Ready` stays False while the CRD is absent, and the Bundle's `GraphReady` and the PolicyGate `bundleSettled` check would wait forever. The payload is untyped, and any CRD event recompiles every dynamic Graph (one per Bundle). kardinal does not adopt it because #1283 removes the health refs. |
| [kro#1293](https://github.com/kubernetes-sigs/kro/issues/1293) and `docs/design/proposals/deferred-schema-resolution.md` | Issue open; design merged, not implemented, written for RGD | Partial | An `optional` marker on a literal GVK, with a GroupKind-scoped re-enqueue. Keeps typed schemas, but the node is still held not-ready, so the Graph `Ready` problem stays. |
| [kro#1243](https://github.com/kubernetes-sigs/kro/issues/1243) optional CRD | Open, answered with dynamic types | Nothing new | Same answer and limits as dynamic types. |
| [kro#1423](https://github.com/kubernetes-sigs/kro/issues/1423) mapper reset for aggregated APIs | Open bug | None | kardinal's health kinds are CRDs, not aggregated APIs. |

**Smallest change, no upstream work yet.** An opt-in for a node whose kind is not served to
count as excluded (like a false `includeWhen`, so it does not hold Graph `Ready`), plus a
GroupKind-scoped re-enqueue. Only worth asking for if #1283 is reversed.

---

## G5: The Graph identity is provisioned outside the Graph

**Need.** kro applies a namespaced Graph by impersonating `spec.serviceAccountName`, or the
namespace's `default` ServiceAccount if that field is unset
(`pkg/controller/graph/impersonation.go:15-58`). The Graph needs a ServiceAccount that can
write PromotionSteps, PolicyGates and PRStatuses in its namespace, and read health objects
in the namespaces its ref nodes point to (`argocd`, `flux-system`, app namespaces).

**kro today.** A Graph cannot create its own identity. It would have to create a
ServiceAccount and RoleBindings while running as that same identity. kro also checks
`watch` with a SelfSubjectAccessReview under the impersonated identity before it opens a
drift watch on a kind (`impersonation.go:157-199`, `CanWatch`); a denied review skips that
watch instead of failing the Graph. Separately, kro's own metadata informers watch
every namespace (`pkg/watch/manager.go:226-232`), so kro itself needs cluster-wide
list/watch on every kind a Graph uses.

**kardinal workaround.**

- `pkg/graph/identity.go` `IdentityProvisioner` creates the `kardinal-graph`
  ServiceAccount, an applier RoleBinding in the Graph namespace, and reader RoleBindings
  named `<readerRole>-<graphNamespace>` in each ref namespace. It runs before every
  Graph create.
- Reader bindings go only into the Graph's own namespace and the allowlist
  `--graph-reader-namespaces` (chart `graph.readerNamespaces`, default `argocd`,
  `flux-system`; `*` allows every namespace; `kube-system`, `kube-public` and `kube-node-lease` are never allowed). Pipeline authors
  choose ref namespaces, so without the allowlist any author could make the controller grant
  read access anywhere. A health ref into another namespace, or one where the bind is
  forbidden, is dropped from the Graph with a warning: kro treats a forbidden ref read as a
  hard error, while health refs are only observational (G3).
- The namespaces that hold a reader binding are recorded in the applier RoleBinding's
  `kardinal.io/reader-namespaces` annotation. `Prune` deletes the bindings that no Graph in
  the namespace reads through any more, after each Graph create and after each Graph delete
  (`pkg/reconciler/graphcleanup`), so the last Graph of a namespace takes its bindings with it.
  When the applier RoleBinding and its record are gone (the namespace was deleted), `Prune`
  looks in the Graph namespace and the named allowlist namespaces. In cluster mode a
  leader-only sweep runs at startup and every 10 minutes: it lists the RoleBindings labeled
  `app.kubernetes.io/managed-by=kardinal-promoter` and prunes every Graph namespace that has a
  reader binding, which also catches bindings from Graphs deleted while the controller was
  down, from older versions, and, with `*`, outside the named namespaces.
- The chart ships the applier and reader ClusterRoles, plus an aggregation ClusterRole
  that gives kro list/watch on kardinal kinds (`chart/kardinal-promoter/templates/graph-rbac.yaml`).
- **Planned:** #1283 (open) removes the health ref nodes and the reader RBAC (ClusterRole,
  RoleBindings, `Prune`, `--graph-reader-namespaces`). Health stays in the PromotionStep
  reconciler.

Verified on kind: `kardinal-promoter-graph-reader-default` in `argocd`, impersonated
applies succeed, and there are no RBAC or watch errors in the kro logs.

**Upstream work.** kro will not create the identity: it would need RBAC `bind` and
`escalate`, which breaks its trust model (`advanced/01-access-control.md:111-124`), and a
Graph cannot grant itself RBAC while running as that identity. So `IdentityProvisioner` stays.

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| [kro#1402](https://github.com/kubernetes-sigs/kro/pull/1402) docs, "`Graph` and ServiceAccount impersonation" (`website/docs/docs/advanced/01-access-control.md:80-220`) | Merged 2026-09-18, in the pin | Partial | Done: our old "document the identity model" ask. It documents the identity string, the SA-in-the-Graph-namespace rule, narrowing `impersonate` with `resourceNames`, and kro's own cluster-wide list/watch through the aggregation ClusterRole. Not covered: controllers that generate Graphs (binding before the first reconcile, teardown order). |
| [kro#892](https://github.com/kubernetes-sigs/kro/issues/892) RBAC isolation for RGDs | Open, needs a KREP | None | RGD only. The Graph kind already impersonates per Graph. |
| [kro#1464](https://github.com/kubernetes-sigs/kro/issues/1464) / [kro#1465](https://github.com/kubernetes-sigs/kro/pull/1465) watches dropped on a hard apply failure | Issue open; PR open, unapproved, lint failing; maintainers lean toward async watch setup | Partial | Stops the informer churn when a Graph hard-fails, for example on `Forbidden`. A `Forbidden` still fails the whole Graph, so `dropHealthNodes` stays. |

**No upstream work.**

- **G5-a: docs for Graph generators.** A subsection under the impersonation docs: create the
  SA and applier RoleBinding before the Graph; keep reader bindings to an allowlist;
  teardown runs as `status.appliedServiceAccount`, so keep old bindings until the Graph is
  gone; deleting a namespace races the binding against kro's finalizer.
- **G5-b: `Forbidden` is an unclassified hard error.** Nothing in `pkg/graphengine` or
  `pkg/controller/graph` checks `apierrors.IsForbidden`. Smallest change: report it as a
  soft not-ready with `ResourcesConverged=False reason=Forbidden` (naming the GVR, namespace
  and identity) on the existing not-ready backoff. kardinal could then delete
  `dropHealthNodes` (`pkg/translator/translator.go:293`) and the `unbound` plumbing in
  `IdentityProvisioner.Ensure`. Moot if #1283 removes the health refs; we plan to raise it
  on kro#1464 anyway ([Engagement](#engagement)).
- **G5-c: cluster-scoped reads (2026-10-08).** A `ref` to a cluster-scoped object needs a
  ClusterRole and ClusterRoleBinding for the Graph ServiceAccount of every namespace that uses
  it, and `IdentityProvisioner` creates only RoleBindings. Two v0.10.0 features avoid it: the
  translator inlines a `ClusterAnalysisTemplate` into the AnalysisRun template (#1444,
  `pkg/translator/analysis.go`), and resolves a `ClusterScmProvider` into a static spec field
  (#1459, **planned in #1517 (not on main)**), the same way it copies PolicyGate templates
  into instances. No upstream ask: the grant is kardinal's to make, and
  a static copy also gives the Bundle a snapshot.

---

## G6: Spec changes are handled by delete-and-recreate

**Status: fixed in kardinal (2026-09-29). This was never a kro gap.**

**Need.** When a Pipeline changes while a Bundle is in flight (#626), the Graph should
pick up the new shape without re-running environments that are already Verified.

**kro today.** kro supports in-place spec updates. It applies the new desired state and
prunes only resources whose nodes were removed, using its managed-resource inventory
(`pkg/controller/graph/controller.go:393-441`). Deleting a Graph runs its finalizer, which
deletes **every** managed resource.

Verified on kind with a scratch ConfigMap Graph:

- Updating the spec kept the existing children, with the same UIDs.
- Removing one node deleted only that node's ConfigMap.
- Deleting the Graph deleted all of them.

**What kardinal did.** `ensurePipelineSpecCurrent` deleted the Graph when the Pipeline spec
hash changed, and the Bundle reconciler recreated it. Under kro that deletes every
PromotionStep, including Verified ones. The recreated steps start Pending, so every
environment promotes again.

**Fix.** `GraphClient.Create` now creates the Graph or updates it in place (spec,
ownerReferences, merged labels, and no write when unchanged). `ensurePipelineSpecCurrent`
re-translates the Graph instead of deleting it (`pkg/graph/client.go`,
`pkg/reconciler/bundle/reconciler.go`). The recreate path is still used when a Graph is
deleted externally (#490).

**Upstream contribution.** None needed.

---

## G7: No ownerReferences on Graph children

**Need.** Find which Bundle a PromotionStep, PolicyGate instance or PRStatus belongs to,
and clean everything up when the Bundle goes away.

**kro today.** Children get no ownerReference. kro stamps the `kro.run/node-id` label and
node-path annotations (`pkg/graphengine/executor/simple.go:1115-1160`) and cleans up with
the Graph finalizer, the managed-resource inventory, and prune
(`pkg/controller/graph/controller.go:233`, `393-441`).

**kardinal workaround.** The Bundle owns the Graph (ownerReference set in
`pkg/graph/builder.go`). Children carry `kardinal.io/bundle`, `kardinal.io/pipeline`
and `kardinal.io/environment` labels, and reconcilers look up their parent by label.
Deleting the Bundle garbage-collects the Graph, and kro's finalizer removes the children.
Deleting the namespace instead deletes the applier RoleBinding kro deletes as, in no set
order, so kro can be left unable to delete anything and keeps its finalizer. For its own
Graphs in a Terminating namespace whose applier RoleBinding is gone, the controller removes
kro's finalizer and lets the namespace deletion remove the children
(`pkg/reconciler/graphcleanup`). The children all live in the Graph's namespace; one kro
records elsewhere would be deleted first.

**Upstream work.** Nothing targets the Graph kind.

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| [kro#1445](https://github.com/kubernetes-sigs/kro/pull/1445) / [kro#1419](https://github.com/kubernetes-sigs/kro/issues/1419) per-resource deletion policy | PR open, in review, not approved | None for the Graph kind | RGD only: `Resource.deletionPolicy: Delete \| Detach` stamped as `kro.run/deletion-policy`. The Graph's `reconcileDelete` (`controller.go:233-284`) ignores the annotation. Even if extended, a detached object keeps the old Graph's template field manager, so a successor Graph applying different values gets `ErrFieldManagerConflict`. |
| [kro#763](https://github.com/kubernetes-sigs/kro/pull/763) KREP-004 ownerReferences and deletion policy | Draft, rotten | None | RGD-to-CRD ownership only. |
| [kro#1332](https://github.com/kubernetes-sigs/kro/pull/1332) background propagation on delete | Open, needs rebase | None | Instance controller only; kardinal's children are CRs and graphcleanup already sets Background. |
| [KREP-014](https://github.com/kubernetes-sigs/kro/pull/1091) / [kro#1264](https://github.com/kubernetes-sigs/kro/pull/1264) lifecycles, retain | Stale; draft on the old code | None | RGD only, predates `graphengine`. |
| [kro#1413](https://github.com/kubernetes-sigs/kro/pull/1413) safe Graph teardown | Closed unmerged 2026-09-16, no follow-up | None | Shows teardown hardening is wanted but unowned. It did not cover a Terminating namespace. |
| [kro#1420](https://github.com/kubernetes-sigs/kro/issues/1420) instance inventory | Open | None (the Graph has it) | The Graph already has `status.managedResources`, which graphcleanup reads. Hazard only if kro renames or reshapes it. |

**No upstream work.**

- **G7-a: teardown wedges in a Terminating namespace.** `reconcileDelete` deletes as the
  applied SA and keeps `kro.run/graph-finalizer` on any error; the namespace controller can
  delete the applier RoleBinding first. Smallest change: when the Graph's own namespace is
  terminating, skip `managedResources` entries in that namespace (the namespace controller
  deletes them), delete the rest as today, then drop the finalizer. That is what
  `pkg/reconciler/graphcleanup` does; if it lands, kardinal deletes its finalizer-removal half
  and keeps the reader-binding `Prune`. We plan to open this as a kro issue
  ([Engagement](#engagement)).
- **G7-b: a template cannot reference its own Graph.** `graph` is a reserved node ID
  (`compiler/validation.go:48`) but is not bound to the Graph object. Low priority.

**kardinal option, no kro change.** Templates can set an ownerReference to the Bundle
(`uid: ${bundle.metadata.uid}` from the `bundle` ref node), with `controller: false` and no
`blockOwnerDeletion` (which would need `update bundles/finalizers` for the impersonated
SA). That gives `kubectl tree` and owner lookup, not G7-a. The old
`spec.childOwnerReference` ask is dropped.

---

## G8: Logic still outside the Graph

These pieces are still Go code in reconcilers. Each is a candidate for a Graph primitive.
The detailed tracker is `docs/design/11-graph-purity-tech-debt.md`.

| Logic | Where | Why not in the Graph | Possible kro primitive |
|-------|-------|----------------------|------------------------|
| Wall-clock time (`schedule.*`, soak) | `ScheduleClock` reconciler; `soakMinutes` in `pkg/reconciler/bundle/reconciler.go` `handleSyncEvidence` | CEL in kro has no `now()` and no time-based requeue | `time.now()` from KREP-025 (in review) covers soak; recurring windows need its follow-up calendar KREP |
| Soak time (`bundle.upstreamSoakMinutes`) | Bundle reconciler writes `status.environments[].soakMinutes` and requeues every minute while Promoting; PolicyGate reconciler takes the minimum over the gated environment's direct upstreams | Same as above; also a cross-node read | KREP-025, reading `uat.status` from the gate node |
| PolicyGate CEL (`bundle.*`, `schedule.*`, `metrics.*`, `upstream.*`) | `pkg/reconciler/policygate` | Time and explainability. A Graph can already read every data source a gate uses (selector refs from KREP-003 decorators, reshaped by a `def` node), and `changewindow.isAllowed/isBlocked` are sugar for a map index. What it cannot do: time-derived fields (metric staleness, active ChangeWindows), and recording why a gate blocked (`status.reason`, `lastEvaluatedAt`, the audit that `kardinal explain` shows) | A clock in CEL (KREP-025). Explainability needs no kro change: the PolicyGate CR plus its reconciler, which writes `status.ready`, is the intended shape (an owned node) |
| Git and SCM steps (clone, kustomize, push with rebase-and-retry onto a branch other Pipelines move, rebuilding an open PR's branch when its base moves (#1461), open PR, merge detection) | `pkg/steps`, `pkg/scm`, PRStatus reconciler | Side effects on external systems; kro only applies Kubernetes objects. A git working tree cannot cross a reconcile boundary, so clone, edit, commit, rebase and push stay in one step sequence | Out of scope for kro. The PromotionStep CR is the Graph-native boundary |
| Artifact discovery: registry, Git and Helm polling with credentials, tag filters, `pathGlob`, and the registry/SCM webhook receiver (#1454, #1455) | Subscription reconciler (`pkg/reconciler/subscription`, `pkg/source`), `/webhook/subscriptions/...` in `cmd/kardinal-controller` | External I/O before any Graph exists: a Graph is per Bundle and the Subscription creates the Bundle; a `ref` node reads only Kubernetes objects, and kro CEL has no I/O. The reconciler is an owned node (writes only Subscription status, creates Bundles, which enter the Graph flow); the receiver writes only the `kardinal.io/refresh` annotation, like the SCM webhook writes only PRStatus | None needed. The Bundle CR is the Graph-native boundary |
| Outbound notifications (NotificationHook webhooks: json, Slack, Teams, templated bodies) and the controller egress allowlist | `pkg/reconciler/notificationhook`, `pkg/egress` | An HTTP POST to an external system is a side effect, and a delivery record must survive restarts; kro only applies Kubernetes objects. The hook is not a Graph node: it reads the status that Bundle, PolicyGate and PromotionStep reconcilers already write (phase, `Ready` condition and its `Unblocked` reason, `prURL`, state) and writes only its own status (`processedEventKeys`, conditions). The allowlist is controller configuration, not promotion logic | None needed. A Graph-level event sink would still need a delivery controller; no ask |
| Health adapters (HealthChecking to Verified) | `pkg/health/adapter.go` via PromotionStep reconciler | A Graph cannot write PromotionStep status, and `readyWhen` does not gate dependents (G1, G3) | None needed: stays in the reconciler by design (#1283) |
| MetricCheck query slots (`Limiter`, #1479) | `pkg/reconciler/metriccheck/limiter.go` | Rations outbound queries to user-chosen endpoints (per namespace and cluster-wide, FIFO, wake-ups through a channel source). Process-local: it holds no promotion state, every result is written to MetricCheck status, and a restart only makes the checks ask again. Approved as an exception to the in-memory-state rule (coordinator, as the owner's delegate, 2026-10-09) | None needed: concurrency control of side effects, not promotion logic |
| Remote-cluster health (`health.kubeconfigSecretRef`, #1458) | `pkg/health/remote.go` (`RemoteClusters`), `healthDetector` in `pkg/reconciler/promotionstep` | kro reads only the cluster it runs in: a ref node cannot point at another cluster, and the remote-cluster KREPs (KREP-012/013, kro#1060, kro#591) are RGD only and rotten or frozen. The translator leaves out the health ref node of such an environment (a ref to an object that is not in this cluster would hold the Graph) | A per-node kubeconfig reference for ref nodes, with the same rules (inline credentials only, https servers only, namespace-local Secret). No ask yet: hub-side Argo CD and Flux cover most users. The process-local client cache (`RemoteClusters`, LRU 128, TTL 1h) is approved as an exception to the in-memory-state rule (coordinator, as the owner's delegate, 2026-10-09): it holds no promotion state, every decision is written to the PromotionStep status, and a restart only rebuilds the clients |
| Gate results as SCM commit statuses while a PR waits for merge (#1452; **Planned in #1518 (not on main)**) | PromotionStep reconciler posts `kardinal/gates`; the gate results reach the step through a mirror `patch` node (G14). Neither is on main | Side effect on an external system | Out of scope for kro |
| Holding an existing PromotionStep: pause (freeze gate) and the required-gate re-check before the step starts (#1300, #1313) | `holdIfPaused` and `checkRequiredGates` in `pkg/reconciler/promotionstep` | No primitive to hold an existing node without pruning it. `readyWhen` does not hold dependents in a standalone Graph; an Unresolved node leaves the existing object as it is; `includeWhen: false` prunes it; a ref to a missing object holds the whole Graph | A Graph-level "hold" on a node that keeps its object and blocks its dependents, or `GateReadiness` for standalone Graphs |

**Upstream work: time.**

| Item | State (2026-10-02) | Coverage | How and why |
|------|--------------------|----------|-------------|
| [KREP-025 Time](https://github.com/kubernetes-sigs/kro/pull/1376) | Open; approved by jakobmoellerdev 2026-09-18, waiting on a second review (cheeseandcereal assigned) | Partial | `time.now()` with comparisons the runtime solves for the instant the result flips, and a requeue at that instant. Covers soak (`time.now() - timestamp(uat.status.completedAt) >= duration("30m")`), ChangeWindows with absolute start and end, override expiry and metric staleness. Does not cover `schedule.*` or recurring windows ("Mon-Fri 09:00-17:00 Europe/Berlin"): `getHours`/`getDayOfWeek` are rejected and calendar and timezone helpers are deferred to a follow-up KREP. |
| [kro#1434](https://github.com/kubernetes-sigs/kro/pull/1434) KREP-025 implementation | Draft | Partial | Only the RGD instance controller requeues at the flip (`rt.TimeRequeueAfter()`); the Graph controller does not, so a time-based `includeWhen` stays false until an unrelated event and a time-based `readyWhen` opens up to ~5 minutes late. The author confirmed on 2026-10-02 (Slack) that this is a gap in the draft, not the design, and the finished implementation should requeue Graphs too. The per-instance rate limiter is not implemented yet. Reserves `time` as a node ID. |
| Calendar and timezone helpers | Deferred to a follow-up KREP (author, 2026-10-02); nothing filed | None | We suggested a predicate, `time.now().inWindow(days, start, end, tz)`, that also records its next flip, in our feedback on kro#1434. |

**Upstream work: CEL functions and data.** Nothing helps, and nothing is needed beyond time.

| Item | State | Coverage | Why |
|------|-------|----------|-----|
| KREP-003 decorators (`docs/design/proposals/decorators.md`), selector refs | Merged, in the pin | Partial | A Graph can list MetricChecks, ChangeWindows, PRStatuses and sibling Bundles. Each new kind needs impersonated list/watch, and ChangeWindow is cluster-scoped, which needs a ClusterRoleBinding `IdentityProvisioner` does not make. |
| [KREP-011](https://github.com/kubernetes-sigs/kro/pull/1035) / [kro#1225](https://github.com/kubernetes-sigs/kro/pull/1225) variables, [kro#1008](https://github.com/kubernetes-sigs/kro/issues/1008) locals | Stale | None | RGD parity for the `def` node the Graph kind already has. |
| [kro#920](https://github.com/kubernetes-sigs/kro/issues/920) global variables | Needs a KREP | None | Leaning toward "use a ref to a ConfigMap"; kardinal's context is per Bundle anyway. |
| [kro#361](https://github.com/kubernetes-sigs/kro/issues/361) custom functions | Closed, rotten | None | No path exists for adopter-registered CEL functions; kro's environment is fixed (`pkg/cel/environment.go:128-156`). We do not need one. |
| [kro#643](https://github.com/kubernetes-sigs/kro/issues/643) CEL versioning | Needs a KREP | None | KREP-009 records the CEL version only per RGD GraphRevision. kardinal pins kro, so drift comes only with an upgrade. |
| [kro#1258](https://github.com/kubernetes-sigs/kro/issues/1258) kro as a library | Open, no labels | None | Evaluating gates with kro's runtime inside kardinal is still logic outside the Graph, and needs the banned `kro/api` import. |

**Upstream work: multi-cluster.** [KREP-012](https://github.com/kubernetes-sigs/kro/pull/1064),
[KREP-013](https://github.com/kubernetes-sigs/kro/pull/1223),
[kro#1060](https://github.com/kubernetes-sigs/kro/issues/1060) and
[kro#591](https://github.com/kubernetes-sigs/kro/issues/591) are RGD only and rotten or frozen:
None. kardinal checks remote workloads through hub-side Argo CD and Flux objects, or reads the remote cluster itself from the PromotionStep reconciler with a kubeconfig Secret (`health.kubeconfigSecretRef`, see the G8 table), so no ask.

**Upstream work: hold and suspend.**

| Item | State | Coverage | Why |
|------|-------|----------|-----|
| [KREP-006](https://github.com/kubernetes-sigs/kro/pull/861) `propagateWhen` | Stalled (see G1) | Partial | A false `propagateWhen` freezes the node. In krocodile it also blocks prune and holds dependents. It cannot tell the node's own controller to stop, so `holdIfPaused` stays. |
| RGD suspend ([kro#1221](https://github.com/kubernetes-sigs/kro/pull/1221), [kro#1282](https://github.com/kubernetes-sigs/kro/pull/1282)) | Merged | None for Graph | `kro.run/reconcile: suspended` is read only by the instance controller. |

**Smallest change, no upstream work yet:** the Graph controller honors
`kro.run/reconcile: suspended` (in `Reconcile`, after the deletion branch: set `Ready=False`
with reason `ReconciliationSuspended`, skip apply and prune). About 30 lines, ported from the
instance controller. It would let a Superseded Bundle's Graph freeze without the
`resolvableWhen` hold on `bundle.status.phase`. We plan to open it as a kro issue
([Engagement](#engagement)).

**Git and SCM:** out of scope for kro by design. No ask.

---

## G9: A Graph reconcile costs three API calls per object

**Need.** A promotion with 100 or more environments, several Bundles in flight, and every other
Pipeline in the cluster still moving at normal speed.

**kro today.** Every reconcile walks every node and, for each object it applies, makes an
uncached `GET` (`executor/simple.go` `getLive`, `:1092`), an uncached SelfSubjectAccessReview
before it registers the drift watch (`controller/graph/impersonation.go:203` `canWatchFor`,
called from `simple.go:1421` `watchObject`), and a server-side apply `PATCH`, with no skip when
nothing changed. The impersonated client is a plain `client.New` (`cmd/controller/graphengine.go:87-90`).
Graphs are reconciled one at a time by default (`--graph-concurrent-reconciles=1`,
`cmd/controller/main.go:142`), with client QPS 100 and burst 150 (`:172`).

Measured on kind (kro v0.10.0-rc.0, 2026-10-08): after a 150-environment run the kro client had
made about 130,000 GETs, 130,000 PATCHes and 131,000 POSTs (the access reviews). A linear chain
advances one environment per reconcile: 4.4 s median per environment at 150 environments
(1,050 objects), 700 s for the whole chain. While two such Graphs reconciled, a three-node Graph
took 4 to 29 s to react to a change, against about 0.1 s when idle; with 8 workers it took 0.1 to
0.3 s.

**kardinal workaround.** `hack/install-kro.sh` sets `config.graphConcurrentReconciles=8`,
`config.clientQps=300` and `config.clientBurst=500` (overridable; `docs/installation.md`
§Install kro). Graphs stay small: gate instances and PRStatuses are `forEach` collections
(`pkg/graph/gates.go`), whose items kro applies 20 at a time (`--apply-concurrency`). The
PolicyGate reconciler writes a gate's status only when its result changes, a step needs a fresh
result, or every `--gate-status-heartbeat` (10m), instead of on every evaluation.

The compact shape (G10) also costs CEL on every walk: `PromotionWave` checks, for each environment,
`e.upstreams.all(u, u in verified)` and `e.gates.all(g, g in readyGates)` over lists, so a walk is
O(environments × (upstreams + gates) × environments) list lookups; a 50-way fan-in into one of
300 environments is about 15,000 comparisons for that entry. kro's CEL cost limit is off by default
(`--cel-cost-limit=0`); a cluster that sets one must allow for it.

**Upstream work.** None filed. [kro#1324](https://github.com/kubernetes-sigs/kro/issues/1324)
(30 s watch-sync block per reconcile) is related: it also stalls every Graph behind one.

**Smallest changes, no upstream work yet.** Cache `CanWatch` per (identity, GVR, namespace) with
a short TTL; skip the SSA when the rendered object hashes to what was last applied (an
annotation); read the live object through the metadata informer kro already runs. Each removes a
third of the calls. A default above 1 for `graph-concurrent-reconciles` is worth asking on
kro#1324.

---

## G10: A Graph is one etcd object

**Need.** Pipelines with hundreds of environments.

**kro today.** The Graph object holds the spec kardinal writes and kro's
`status.managedResources` inventory, one entry per applied object (`api/v1alpha1/graph_types.go:80-87`,
`maxItems: 5000`). etcd refuses a write over 1.5 MiB (`etcdserver: request is too large`); the API
server's gRPC limit is 2 MiB. There is no node-count limit in the compiler. The Graph kind has
no GraphRevision, so there is no second copy of the spec. When a write does not fit, the API
server drops `metadata.managedFields` and retries (`k8s.io/apiserver`
`pkg/endpoints/handlers/update.go:229-236`), so managedFields do not count against the limit,
but every write near it fails once at etcd first.

Measured with Graphs from kardinal's builder (one node per object): 1.1 KB of spec per
environment for its PromotionStep and PRStatus, 1.1 KB per PolicyGate instance, 242 bytes of
status per applied object. 150 environments with 3 gates and 2 hook Jobs each (1,051 nodes)
were 1,119,167 bytes once everything was applied. 300 environments (1,716,336 bytes of spec)
were refused at create. The ceiling with 3 gates per environment is about 280 environments,
with 3 gates and 2 hooks about 210.

**kardinal workaround.** `pkg/graph/size.go` `CheckSize`, called by the translator before the
Graph is written: the JSON size plus 260 bytes per template node may not exceed 1,200,000
bytes (collections count one entry per item). Over it, the Bundle fails with `GraphBuildFailed`
and a message that names the size and the fix (a new Bundle or a Pipeline edit retries). Gate
instances and PRStatuses are `forEach`
collections over a `def` node (`pkg/graph/gates.go`): 150 environments with 3 gates each went from
646,536 to 471,305 bytes of spec. Above 100 environments (`--graph-compact-above`, or the
Pipeline annotation `kardinal.io/graph-shape`) the Graph is compact (`pkg/graph/compact.go`): the
promotion DAG is data in a `def` node, the PromotionSteps are one collection admitted by a `def`
over the steps read back through a selector `ref` (the G11 pacing pattern), and there are no
health ref nodes. The Graph has 9 to 12 nodes whatever the environment count. Measured on kind: 300
environments (30 waves of 10, a gate each) promoted end to end in 8 minutes, with the applied
Graph at 472,213 bytes; estimated 0.9 MB with 3 gates each. Graphs also may not create more than
4,500 objects (kro's inventory holds 5,000). The Pipeline CRD allows 500 environments (#1473).

**Upstream work.** None filed. Optional ask: keep the inventory out of the Graph object (an
ApplySet-style parent or a child object), so the spec alone bounds the size.

---

## G11: Collections are all-or-nothing

**Need.** Fan one environment out to many targets (fleets, #1457) and pace them, and keep
gate and PRStatus instances in collections (G10).

**kro today.** A node is rendered row by row and the first data-pending field returns from
`Resolve` (`runtime/node.go:311-316`, `:358-372`), so one pending item leaves the whole
collection Unresolved: no item is applied or pruned. There is no per-item gating. Every item is
stamped with `kro.run/collection-size` (`executor/simple.go:1129-1137`), so a collection that
grows by one item rewrites every item's labels. The same holds for apply errors: when one item
cannot be applied (a ResourceQuota, a denying admission policy, throttling), `applyTemplate`
returns before `publishScope` (`executor/simple.go:375-400`, `applyCollectionTemplate`
`:945-973`), so the collection is not in scope and every node that references it stays
data-pending, however many items did apply.

Measured on kind: the first item's `resourceVersion` changed each time a 300-item step
collection grew (sizes 91, 95, 99), and kro routed 45,814 PromotionStep events over one
300-environment run.

**kardinal workaround.** Pacing is done by choosing the list: a `def` node computes the items
to admit from a selector `ref` that reads the collection's own objects back (no CEL edge, so no
cycle), and items already admitted stay in the list, so pacing never prunes. Verified on kind
with `maxConcurrent` and `maxUnavailable`. kardinal's reconcilers must ignore label-only updates
on the objects they own, or they reconcile every item on each growth.

Fleets (#1457) ship on this pattern, in the compact shape (`pkg/graph/compact.go`). Each DAG entry
carries `fleet`, `index`, `maxConcurrent` and `maxUnavailable`. `PromotionState` adds
`startedFleets`, `verifiedFleets` and `failedFleets`, read from the `kardinal.io/fleet` label of
the observed steps. `PromotionEligible` holds the ready entries without a step, and
`PromotionWave` admits the started entries plus each fleet's eligible entries ranked below its
free places, while fewer than `maxUnavailable` of its targets have Failed. A target removed from
the list leaves the wave; kro prunes its step, whose finalizer closes the PR. The PromotionStep
reconciler ignores kro's label-only updates (`eventfilter.LabelChangedExceptKro`). Selector
membership (Argo CD Applications, ClusterProfiles) is not a Graph collection `ref`: that would need
reader RBAC on the Applications' namespace, which #1283 removes from the Graph identity. The
Pipeline reconciler lists the selected objects and writes only its own `status.fleets`. The
translator builds the Graph from that field, and the Bundle reconciler updates a Bundle's Graph in
place when it changes (`pipelineSpecHashFor`). Membership changes are seen within a minute,
because nothing watches Applications, whose CRD may not be installed.

In the compact shape (G10) the PromotionSteps are one collection too, so the blast radius is the
whole Bundle: one gate instance or step item that kro cannot apply, or that stays soft not-ready,
holds every environment, not only its own.

For apply errors, the builder keeps the blast radius to what must wait anyway: steps reference
the PolicyGates collection (a gated step waits on its gates), so one gate instance that cannot be
created holds every gated environment of the Bundle; the Bundle reconciler then sets
`GatesCreated=False` with the missing instance names and kro's message
(`pkg/reconciler/bundle/gates_created.go`). Steps name their PRStatus literally, not through the
PRStatuses collection, so a PRStatus that cannot be created holds only its own environment, whose
step waits in WaitingForMerge with a message that names it.

**Upstream work.** None filed.

**Smallest changes, no upstream work yet.** A per-item option to skip an item whose fields are
pending (keep its existing object); publish a collection's scope with the items that applied and
report the failed items (or an opt-in to skip failed items), so one bad item does not hold every
dependent; make the `collection-size` label optional or drop it, since `collection-index` and
`node-id` already identify the item.

---

## G12: Delete and prune orphan the Pods of a Job

**Need.** Run a Job before or after an environment's deploy (database migrations, integration
tests) once per Bundle and environment (#1443).

**kro today.** `executor/simple.go:530-555` (`Delete`, used for prune and teardown) sends a UID
precondition and no `propagationPolicy`. For `batch/v1` Jobs the API default orphans the Pods.
A template node also re-creates an object that disappears, and a Job's pod template is
immutable.

Verified on kind with a Job template node: deleting the Graph deleted the Jobs and left their 3
Pods; deleting a completed Job made kro create it again, and it ran again; changing the Job's
command in the Graph failed with `spec.template: ... field is immutable`, a hard error that stops
prune and release for the whole Graph until the change is reverted.

**kardinal workaround.** Hooks are `HookRun` objects (a kardinal CRD) in the Graph. The HookRun
reconciler creates the Job with a controller ownerReference, so garbage collection deletes the
Pods, records a terminal phase once and never runs the Job again, and ignores spec changes after
the Job started (`pkg/reconciler/hookrun`, `pkg/graph/hooks.go`, #1443; live tests
`TestStep_Hook*`).

**Upstream work.** None filed.

**Smallest change, no upstream work yet.** Delete and prune with
`propagationPolicy: Background` (the executor owns the objects it deletes), or a per-node
`deletionPropagation`. Re-creating a run-once kind is by design; documenting it would help.

---

## G13: The Graph controller cannot be sharded

**Need.** Spread promotion work across controller replicas (#1462).

**kro today.** One kro leader reconciles every Graph in the cluster from one queue
(`controller/graph/controller.go` `SetupWithManager`); there is no label selector or shard flag
for Graphs.

**kardinal workaround.** kardinal shards only its own controllers, by namespace label
(`--namespace-shard`, `pkg/shard`, #1462): a per-namespace token Lease `kardinal-shard` decides which
installation reconciles the namespace, and a per-shard heartbeat Lease says whether that
installation is alive. Every shard's Graphs still go through the one kro leader and
its one queue, so Graph throughput stays bounded by that instance (G9) and its
`graphConcurrentReconciles`.

**Upstream work.** None filed. Ask: a `--graph-selector` label selector on the Graph
controller, so several kro installations can split Graphs.

---

## G14: A node with one pending field is wholly Unresolved

**Need.** Give an existing PromotionStep live data from other nodes (gate results while its PR
waits for merge, hook and analysis results, image verification) while its template also carries
the `resolvableWhen` gating fields of G1.

**kro today.** One data-pending field makes the whole node Unresolved (`runtime/node.go:358-372`),
and an Unresolved node is not re-applied (`executor/simple.go:318-323`). Once a gate turns false
after the step exists, the step's template is frozen. `TolerateDataPending`, which omits a
pending field and applies the rest, is set only for the RGD adapter's status node
(`compiler/compiler.go:304`, `compiler/program.go:100-104`).

**kardinal workaround.** A `patch` node per environment whose target is the step's literal
name (not `${step.metadata.name}`, which is Unresolved with the step) writes the live data
onto the step. A patch whose target does not exist yet is a soft not-ready, and a patch may
target an object a template node of the same Graph owns; the two field managers coexist.
Verified on kind: the mirrored gate result followed the gate (true, false, true) while the step
node was Unresolved. Hooks use it (`live0<env>` writes `spec.live.hooks`, `pkg/graph/hooks.go`,
#1443); gate commit statuses are **planned in #1518 (not on main)** to use it as well.

**Upstream work.** None filed.

**Smallest change, no upstream work yet.** Let a Graph node opt into `TolerateDataPending`, or
per field. The G1 `gateReadiness` opt-in would also do: the gating fields would go away.

---

## G15: kro holds every Graph in memory, and a Graph cannot be retired without its children

**Need.** Keep the history of a finished Bundle (its PromotionSteps, which rollback, promote,
history, DORA metrics, the Pipeline phase, the CLI and the UI read) without keeping its Graph
live. kro was OOMKilled at about 337 Graphs with its 1 GiB default (#1492): kardinal kept the
Graph of every finished Bundle until `historyLimit` pruned the Bundle, up to 50 per Pipeline.

**kro today** (v0.10.0-rc.0, read in the source):

- A live Graph costs memory for its whole life: the compiled program in the `Registry`
  (`pkg/graphengine/registry/registry.go`), its watch registrations in the `watchrouter`, and its
  `SchemaWatcher` subscriptions. They are released only in `reconcileDelete`
  (`pkg/controller/graph/controller.go:262-269`).
- `reconcileDelete` deletes every `status.managedResources` entry by name with a UID
  precondition (`executor/simple.go:530-570`). It reads no label, annotation or ownerReference
  (kro sets none on children, G7), and there is no deletion policy or suspend for the Graph
  kind: `kro.run/reconcile: suspended` is read only by the instance controller
  (`pkg/controller/instance/controller.go:317`), and kro#1445's `Detach` is RGD-only.
- The cost is per Graph, not per byte: the scale suite measured 5.4 MB of kro working set per
  live Graph for 14 KB, 11-node Graphs (148 Graphs, 844 MiB; OOMKilled at about 160 in 1 GiB).
  A heap profile at 150 Graphs (kro built with `-tags pprof`) has 390 MB in use, 88% of it in the
  compiled programs the `Registry` keeps (`Registry.Compile` → `compileFrame`). 83% is the typed CEL
  environment (`krocel.TypedEnvironmentWithIDsAndProvider` → `defaultEnvironment`,
  `pkg/cel/environment.go:218`): `SchemaDeclTypeWithMetadata` (`pkg/cel/schemas.go:52`, 52%)
  converts every typed node's CRD schema into a fresh `DeclType`, and `MaybeAssignTypeName` (25%)
  copies it under the node's name. None of it is shared between nodes of the same kind or between
  Graphs. That is about 2.6 MB of heap per Graph, roughly 5 MB of working set at GOGC=100.
- Shrinking the spec prunes the removed nodes' resources (`controller.go:391-425`); entries of
  an Unresolved node are kept, but the node stays compiled, so the memory stays.

**Options checked and rejected.**

- *Detach by removing kro's finalizer, then delete.* The children survive, but a Graph deleted
  without the finalizer path leaves its `Registry` entry, watches and schema subscriptions in
  kro: `Reconcile` returns on NotFound (`controller.go:129-131`) without releasing them, so the
  memory is not freed until kro restarts.
- *Detach by clearing `status.managedResources`, then delete.* No race-free order exists: an
  in-flight reconcile writes its in-memory inventory back with an unconditional merge patch
  (`updateStatus`, `controller.go:707-740`; `persistManagedResources` only grows it), and every
  later reconcile re-records each applied child. If that write lands after ours, teardown deletes
  the children anyway. It also means writing kro's status, a cross-CRD status write.
- *Shrink the Graph to a minimal spec.* kro prunes the children (the same loss as deleting) and
  the Graph object stays.

**kardinal workaround.** Retire, with a record. After a delay (1m for a Superseded Bundle or a
Verified one replaced everywhere, 1h for a Verified one still deployed, 24h for a Failed one;
`--graph-retire-*-after`, Pipeline annotation `kardinal.io/graph-retire-after`), and once every
step is `Verified` or `Failed` with no finalizer, the Bundle reconciler writes one record per step
to `Bundle.status.retiredSteps` and sets `GraphRetired=True` in one status write, then deletes the
Graph; kro deletes the children. Readers list steps with `lifecycle.ListPromotionSteps` /
`AddRetiredSteps`, which add the records of retired Bundles as in-memory steps. A retired Bundle
is final (no recovery, no rebuild on a Pipeline change). Lost: gate instances, PRStatuses,
per-step detail and Events; AuditEvents are not Graph children and stay. kro's memory is then
bounded by the Bundles in flight and recently finished; `hack/install-kro.sh` sets the limit
(`KRO_MEMORY_LIMIT`) and `docs/installation.md#sizing-kro` sizes it.

**Upstream asks** (drafts, not posted):

- Graph honors `kro.run/reconcile: suspended` (skip apply and prune). With it, kardinal could
  suspend a finished Graph, wait for the condition, clear its inventory and delete it, keeping
  the children race-free.
- A Graph-level or node-level `Detach` deletion policy in the shared teardown (comment on
  kro#1445), which releases the template field manager on detach.
- Release the `Registry` entry, watches and schema subscriptions when `Reconcile` finds the Graph
  gone (`controller.go:129-131`), so a Graph deleted without the finalizer does not leak.
- Cache the `DeclType` per CRD schema (GVK and resourceVersion) and share it across nodes and
  Graphs instead of converting and renaming it per node. Draft #10 in the upstream drafts, not
  posted.

---

## Notes: constraints we design around

These are not gaps, but the translator has to work around them.

- **Node ID grammar.** IDs must match `^[A-Za-z][A-Za-z0-9]*$` and must not be reserved
  (`pkg/graphengine/compiler/validation.go:28-51`: `apiVersion`, `kind`, `metadata`,
  `namespace`, `spec`, `status`, `graph`, `graphengine`, `kro`, `each`, `item`, `items`,
  `object`, `self`, `this`, `context`, CEL keywords).
  The PromotionStep node ID is the environment name in camelCase (`test`, `uat`,
  `prod`); PRStatus, health and gate nodes carry a prefix. kardinal also uses `bundle` as
  a node ID. Pipeline validation rejects these names. The API server refuses a reserved
  environment name (`api/v1alpha1/pipeline_types.go`), and `pkg/graph/validate.go` checks
  node IDs. Upstream: [kro#1434](https://github.com/kubernetes-sigs/kro/pull/1434) (KREP-025)
  adds `time` to the reserved IDs, so an environment named `time` would make the Graph invalid
  once it ships, so kardinal reserves it already (`pkg/graph/validate.go`, `api/v1alpha1/pipeline_types.go`). IDs over 63 characters are fine: since
  [kro#1392](https://github.com/kubernetes-sigs/kro/pull/1392) (in the pin) the `kro.run/node-id`
  label value is hashed and the readable ID moves to an annotation. Quickstart gate IDs are
  65 characters; kardinal never selects on that label.
- **No `forEach` on ref nodes** (`validation.go:131-133`). A selector health ref is a
  collection ref with a per-element `readyWhen` on `each`. No upstream work; moot once #1283
  removes the health refs.
- **An empty collection is ready** (`pkg/graphengine/runtime/node.go:240-252`). A
  selector health ref that matches nothing passes Graph readiness. The PromotionStep's Go
  adapter does not: it treats no matching Deployment as unhealthy (`docs/health-adapters.md`).
  No upstream work. In-pin workaround: a `def` node `{count: ${size(coll)}}` with
  `readyWhen: ${check.count > 0}`.
- **The Graph is not Ready while any node is not Ready.** A `readyWhen` on a node that
  can never become ready keeps the Graph `Ready=False` forever. This happened with
  `PRStatus` nodes whose step opened no PR: an auto environment, then (B69) a `pr-review`
  step with nothing to commit, or an `auto` step whose environment was edited to
  `pr-review` while it ran. `PRStatus` nodes now have no `readyWhen`; the step node's
  `state == "Verified"` already comes after the merge when there is a PR.
- **A Graph cannot be suspended.** The `kro.run/reconcile: suspended` annotation
  (`api/v1alpha1/groupversion_info.go:36-55`) is read only by the instance controller
  (`pkg/controller/instance/controller.go:317`), not the Graph controller. Deleting the
  Graph deletes every managed resource (`pkg/controller/graph/controller.go`
  `reconcileDelete`), and a node whose `includeWhen` turns false is pruned
  (`executor/simple.go:289-296`). To stop a Superseded Bundle's Graph while keeping its
  steps and gates, every step node holds on `bundle.status.phase` through `resolvableWhen`
  (G1). No upstream work for the Graph kind; see G8 "hold and suspend" for the ask.
- **Data-pending classification is by error text** (`runtime/errors.go:43-49`). G1's
  workaround depends on `index out of bounds` staying in that list, and on cel-go producing
  that text for `[x].filter(_, false)[0]`. cel-go's list `Get` path uses a different message
  (`index '0' out of range`), and kardinal's `TestBuilder_SupersededHoldIsDataPending` runs
  kardinal's own cel-go, not kro's; both are v0.31.0 today. Check the two versions match on
  every kro upgrade. No upstream work toward a typed data-pending error.
- **Graph `Ready` is a live aggregate.** A gate node's `readyWhen` (`status.ready == true`)
  is re-evaluated on every change, so a gate that turns false after `prod` is Verified
  (a weekend window, a soak threshold raised in place) flips the Graph back to
  `Ready=False` even though nothing is left to do. The PromotionSteps are unaffected.
  Read completion from the Bundle (`Ready` condition, `status.metrics`), not from the Graph.
  No upstream work ([kro#1253](https://github.com/kubernetes-sigs/kro/issues/1253) improves
  readiness messages only). kardinal already latches the Bundle's `GraphReady` once it is
  True (`refreshGraphConditions`). Basing the PolicyGate `bundleSettled` check on the steps'
  terminal state would remove the last dependency on Graph `Ready`.

- **Environment holds (#1528) use the G1 pattern.** `kardinal rollback --hold` writes
  `Pipeline.spec.holds[]` (environment, rollback Bundle, reason, artifact digest, optional
  expiresAt). `graph.Build` reads it, and the held environment's PromotionStep node gets
  `spec.bundleName: resolvableWhen(bundleHeld && bundle.metadata.name == "<rollback>")`
  (`pkg/graph/builder.go` `heldCond`). Every other Bundle's step in that environment is
  data-pending: not applied, not pruned. In the compact shape the environment's
  `PromotionDAG` entry carries `held: true` for every other Bundle, and `PromotionWave` does not
  admit it (`pkg/graph/compact.go`). Its downstream environments wait on it. A hold change is a
  Pipeline spec change, so the bundle reconciler rebuilds every active Bundle's Graph in place
  (`ensurePipelineSpecCurrent`, `pipelineSpecHashFor`).
  Each reconciler that reads the field writes only its own CRD:
  - The PromotionStep reconciler holds steps created before the hold (`holdIfEnvironmentHeld`,
    as for pause). It cancels one waiting for its merge as supersession does, closing the PR
    with a comment.
  - The Bundle reconciler neither supersedes nor garbage-collects the hold's Bundle.
  - The Pipeline reconciler is the only one that reads the clock for holds. It removes an
    expired hold from the spec and writes the `HoldCreated` and `HoldReleased` AuditEvents from
    the difference between `spec.holds` and `status.observedHolds`.
  - The PolicyGate reconciler, which already owns gate status, passes gate instances of the
    held environment for the hold's Bundle, but only after `lifecycle.VerifyHeldRollback`
    checks it against the CRDs. Its `rollbackOf` was Verified there (PromotionSteps); each
    image, config commit and chart was deployed by a Bundle Verified there; and its artifact
    digest equals the one the hold recorded. The reconciler writes an `EXEMPT` reason in
    `status.reason` (or `hold exemption refused: <why>`), audits the flip and emits a Warning
    Event.

  Constraints:
  - The exemption is decided per gate instance from the Pipeline, Bundles and PromotionSteps in
    etcd, with no in-memory state.
  - A kro Graph `readyWhen` could not express it, because the gate's readiness is the PolicyGate
    reconciler's `status.ready` (G3).
  - A Bundle cannot exempt itself. The hold has to name it. Writing a hold needs `update` on
    `pipelines/hold`, which the chart's `<release>-hold-writes` ValidatingAdmissionPolicy
    enforces, together with `createdBy` equal to the caller.
  - The checks cover a Bundle edited after the hold, until Bundle artifacts are immutable
    (#1526).

  What would break this: a kro change to data-pending classification (see above) would let
  held-out steps be applied. `TestBuilder_HeldEnvironment` and `TestCompact_HeldEnvironment`
  evaluate the emitted expressions.

---

## kro upgrade checklist

Run on every kro upgrade (the cadence and the analysis steps are in AGENTS.md §kro Upgrade
Cadence):

1. Set `KRO_VERSION` in `hack/install-kro.sh` and the `kro.version` annotation in
   `chart/kardinal-promoter/Chart.yaml`.
2. **cel-go parity.** Copy the `github.com/google/cel-go` version from kro's `go.mod` at the new
   tag into `KRO_CEL_GO_VERSION` in `hack/install-kro.sh`, and require the same version in
   kardinal's `go.mod`. `TestCelGoParity` (`test/hack`) fails until they match. The G1 workaround
   depends on kro classifying cel-go's `index out of bounds` error as data-pending
   (`runtime/errors.go`), and `TestBuilder_SupersededHoldIsDataPending` checks that text with
   kardinal's cel-go, so the test proves nothing if the versions differ. Also check that
   `celDataPendingPatterns` in kro's `runtime/errors.go` still lists `index out of bounds`.
3. Check the reserved node IDs in kro's `compiler/validation.go` against `reservedNodeIDs` in
   `pkg/graph/validate.go` and the reserved environment names in `api/v1alpha1/pipeline_types.go`.
4. Check the tuning values `hack/install-kro.sh` sets still exist in kro's `helm/values.yaml`
   (`config.graphConcurrentReconciles`, `config.clientQps`, `config.clientBurst`), and re-check
   G9 and G10: the per-object call count and the inventory entry size.
5. Re-read the entries above for anything the new tag closes, and the hazards in the upstream
   survey (`graphcleanup` depends on the finalizer name and the `status.managedResources` shape).

---

## Upstream survey (2026-10-02)

kro `main` at `e1b94df`, 18 commits past the pin (`v0.10.0-rc.0`, `54a203b`). Open and recent
KREPs, issues and PRs were read against each gap; per-gap results are in the **Upstream work**
blocks above.

### Gaps with no upstream work

| Gap | Smallest kro change | Worth asking? |
|-----|---------------------|---------------|
| G1 per-Graph readiness gating | `spec.gateReadiness: true` makes `executorFor` return a `GateReadiness` copy of the executor | Yes: removes `resolvableWhen` |
| G2 skip a node without its dependents | `<id>.excluded()` with non-contagious edges through the other branch | Not now: static skip covers the product |
| G3 cross-node `readyWhen` | Accept references already in the hard-dependency set | No: health stays in the reconciler |
| G4 residual: unserved kind counts as excluded | Opt-in on dynamic types | Only if #1283 is reversed |
| G5-a docs for Graph generators | A docs subsection | Yes, low effort |
| G5-b `Forbidden` as a soft, named condition | Wrap `IsForbidden` as not-ready with reason `Forbidden` | Yes, as a comment on kro#1464 |
| G7-a teardown in a Terminating namespace | Skip same-namespace entries, then drop the finalizer | Yes: removes half of `graphcleanup` |
| G7-b bind the reserved `graph` ID | Bind it to the Graph's metadata | No |
| Graph suspend | Honor `kro.run/reconcile: suspended` | Yes |
| Recurring time windows | Calendar and timezone helpers (follow-up to KREP-025) | Yes, already raised on kro#1434 |
| Empty collection is ready; no `forEach` on refs; typed data-pending error; Graph `Ready` latch | See Notes | No |
| Multi-cluster reads; adopter-registered CEL functions | Read-only `ref.cluster`; a function registry | No |
| G9 walk cost | Cache `CanWatch`; skip unchanged SSA; informer-backed GET | Yes |
| G10 object size | Inventory outside the Graph object | Optional |
| G11 collections | Per-item pending tolerance; optional `collection-size` label | Yes |
| G12 Job orphans | Background propagation on delete and prune | Yes |
| G13 sharding | `--graph-selector` on the Graph controller | Yes, with G9 |
| G14 frozen nodes | `TolerateDataPending` opt-in for Graph nodes | Yes, unless G1 lands first |
| G15 retire a Graph, keep its children | Graph suspend, Graph `Detach` policy, release memory on NotFound | Yes |

### Hazards found

| Hazard | Effect on kardinal | Action |
|--------|--------------------|--------|
| [kro#1434](https://github.com/kubernetes-sigs/kro/pull/1434) reserves `time` | An environment named `time` would make the Graph invalid | Done (2026-10-08): `time` is reserved in `pkg/graph/validate.go` and the Pipeline API |
| [kro#1464](https://github.com/kubernetes-sigs/kro/issues/1464) watches dropped on a hard failure | While one branch keeps hard-failing, a node created in another branch gets no watch, so the Graph waits on controller-runtime backoff (up to ~16 minutes per transition). The hot loop needs a never-committed Graph that is the only user of a kind | Upgrade when kro#1465 or the async watch work lands; comment on kro#1464 |
| [kro#1324](https://github.com/kubernetes-sigs/kro/issues/1324) 30s watch-sync block per reconcile | kro's Graph controller defaults to `--graph-concurrent-reconciles=1`, so one Graph stuck on a kind kro cannot list delays every Graph. Triggered by `graph.aggregateToKro=false` with kro in aggregation mode, or a slow initial list on a large cluster | Set `config.graphConcurrentReconciles` above 1 in `hack/install-kro.sh` and the install docs; document `config.watchSyncTimeout` after the next kro upgrade |
| cel-go version drift | `resolvableWhen` depends on cel-go's `index out of bounds` text (Notes) | Compare cel-go versions on every kro upgrade |
| Teardown identity | kro deletes as `status.appliedServiceAccount`; removing the applier RoleBinding while Graphs exist wedges the finalizer outside a Terminating namespace | Keep the binding until the namespace's last Graph is gone (current behavior) |
| `graphcleanup` coupling | Depends on the `kro.run/graph-finalizer` name and the `status.managedResources` shape | Check both on every kro upgrade |

Checked and not hazards: [kro#1448](https://github.com/kubernetes-sigs/kro/pull/1448)
(includeWhen data-pending, kardinal uses none), [kro#1459](https://github.com/kubernetes-sigs/kro/pull/1459)
(null conditions become false), [kro#1467](https://github.com/kubernetes-sigs/kro/pull/1467)
(dependency paths), [kro#1460](https://github.com/kubernetes-sigs/kro/pull/1460) (RGD prune).

### Upgrade outlook

Nothing between the pin and `main` changes Graph behavior kardinal uses; the only new knob is
`--watch-sync-timeout` (`config.watchSyncTimeout`). There is no `rc.1` or release branch, and
v0.10.0 will likely be cut from `main` as it is. Upgrade the pin when it is tagged; no compat
work is expected. kro#1464 and kro#1324 will probably still be open in v0.10.0.

### Engagement

Nothing has been posted to kro yet. Each item is drafted and waits for owner approval.

| Where | What | Gap |
|-------|------|-----|
| [kro#1434](https://github.com/kubernetes-sigs/kro/pull/1434) | Feedback on the Graph requeue and calendar helpers. Shared with the author on Slack 2026-10-02; the author agreed the requeue is a draft gap and said calendar helpers are deferred to a follow-up KREP | G8 time |
| New kro issue | Graph opt-in readiness gating (`spec.gateReadiness`), offering the PR | G1 |
| [KREP-006](https://github.com/kubernetes-sigs/kro/pull/861) | Ask the KREP to state Graph semantics for `propagateWhen` (Pending, no prune, dependents wait) | G1, hold |
| New kro issue | Graph honors `kro.run/reconcile: suspended`, offering the PR | Hold |
| New kro issue | Graph teardown in a Terminating namespace | G7-a |
| [kro#1464](https://github.com/kubernetes-sigs/kro/issues/1464) | The sibling-branch watch symptom, and `Forbidden` as a named soft condition | G5-b, hazard |
| [kro#1445](https://github.com/kubernetes-sigs/kro/pull/1445) | Scope the docs to RGDs, or honor the policy in the shared teardown; note the field-manager conflict on detach | G7 |
| [kro#1324](https://github.com/kubernetes-sigs/kro/issues/1324) | Same stall on the Graph controller; ask about a default above 1 for `graph-concurrent-reconciles` | Hazard |
| [KREP-018](https://github.com/kubernetes-sigs/kro/pull/1125) (optional) | Data point for Graph adopters | G2 |

When an item is filed, link it from the gap above and from `docs/graph-coverage.md`, and
label the kardinal issue that waits on it `blocked-on-upstream`.

---

## E2E record (kind, 2026-09-29)

| Check | Result |
|-------|--------|
| Graph compiles and is accepted by kro v0.10.0-rc.0 | Pass (12 nodes) |
| Applies run as the impersonated `kardinal-graph` ServiceAccount | Pass |
| `test` then `uat` ordering via G1 workaround | Pass |
| PolicyGate instances created per Bundle, template gates not duplicated | Pass (after fix) |
| `prod` held back by `require-uat-soak` and `no-weekend-deploys` | Pass |
| Auto environments push to the base branch; Argo CD deploys the new image | Pass (after fix) |
| `soakMinutes` advances while a Bundle is Promoting | Pass (after fix) |
| A superseded Bundle's Pending `prod` step is cancelled, with no push and no PR | Pass (after fix) |
| kro logs: no `forbidden` or watch errors under impersonation (G5) | Pass |
| Probe: `forEach` expansion; `size(x) == N && x.all(...)` holds a dependent back | Pass |
| Probe: a selector ref that matches nothing is ready | Confirmed (see Notes) |
| Probe: in-place spec update keeps child UIDs; removing a node prunes only that child; deleting the Graph deletes all children | Pass (G6) |
| `prod` (pr-review): step created once both gates are ready, PR opened on the demo repo, merge detected by PRStatus, step Verified, Graph `Ready=True` | Pass (PR #18) |
| Bundle `status.metrics.commitToProductionMinutes` set once every environment is Verified | Pass (after fix: patch base was taken after the write) |
| PR body lists the gates evaluated and upstream verification | Pass (after fix: fields were never populated) |
| Bundle with `prod` still gated is not marked finished: no metrics, no `Ready=True`, soak clock keeps running | Pass (after fix: "all environments Verified" was computed over `status.environments`, which lacks environments whose step does not exist yet) |
| Pipeline change while Promoting: Graph updated in place (same UID, generation 2), gate instances re-rendered in place, `test`/`uat` steps keep UID and Verified, no re-push to the demo repo | Pass (G6 fix) |
| Second Pipeline change after `prod` Verified (restore): Graph generation 3, same UID, every child UID unchanged, all steps still Verified | Pass |
| Reconciler log lines (`pipeline spec changed — updating Graph in place`, `graph updated in place`) visible in the controller log | Pass (after fix: no zerolog logger was attached to the reconcile context, so reconciler logs were dropped) |
| Four no-op Pipeline changes leave the Graph generation unchanged | Pass (after fix: gate templates were listed in cache map order, so gate nodes swapped position between renders and each re-translation wrote a new spec) |

---

## Closed

- **G6** (2026-09-29): not a kro gap. kro v0.10.0-rc.0 updates a Graph in place and prunes
  only removed nodes; kardinal now issues create-or-update instead of delete-and-recreate.
