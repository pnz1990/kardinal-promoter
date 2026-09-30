# Graph Capability Ledger

> Status: Active. This is the single list of places where the kro Graph does not do what kardinal needs.
> Target: kro `kro.run/v1alpha1` Graph, [v0.10.0-rc.0](https://github.com/kubernetes-sigs/kro/releases/tag/v0.10.0-rc.0) (`54a203b`), `GraphKind` feature gate on.
> Docs: <https://kro.run/next/docs/concepts/graph/overview/>
> Last verified: 2026-09-29, kind e2e (kind v1.33, kro v0.10.0-rc.0, Argo CD v2.10)

---

## How to use this document

The goal is for kardinal to be built entirely on the Graph. Every piece of promotion
logic that lives outside the Graph, and every workaround kardinal applies to make the
Graph behave, is logged here with:

- **Need**: what kardinal needs from the Graph.
- **kro today**: what v0.10.0-rc.0 actually does, with source evidence (`path:line` in
  `kubernetes-sigs/kro` at the tag above).
- **kardinal workaround**: what we do instead, and where.
- **Upstream contribution**: the smallest kro change that would let us delete the workaround.

Rules:

1. Before adding logic outside the Graph, check this list. If the gap is new, add an entry
   first (Constitution Article XII, `docs/design/10-graph-first-architecture.md`).
2. When kro ships a fix, verify it on kind, delete the workaround, and move the entry to
   **Closed** with the kro version.
3. GitHub issues blocked on one of these entries carry the `blocked-on-upstream` label.
4. The public summary of this ledger is [Graph Coverage](../graph-coverage.md). When an
   entry opens, changes or closes, update that page in the same PR.

## Summary

| ID | Gap | Severity | Workaround lives in |
|----|-----|----------|---------------------|
| [G1](#g1-readywhen-does-not-gate-dependents-in-a-standalone-graph) | `readyWhen` does not gate dependents in a standalone Graph | High | `pkg/graph/builder.go` `resolvableWhen` |
| [G2](#g2-includewhen-is-contagious) | `includeWhen` is contagious to every dependent | Medium | `pkg/graph/builder.go` static `skipEnvironments` filter |
| [G3](#g3-readywhen-may-only-reference-the-node-itself) | `readyWhen` may only reference the node itself | High | health on separate ref nodes; revision check impossible |
| [G4](#g4-a-missing-crd-fails-the-whole-graph) | A missing CRD fails the whole Graph compile | Medium | `pkg/translator/translator.go` `servedKind` filter |
| [G5](#g5-the-graph-identity-is-provisioned-outside-the-graph) | The Graph's ServiceAccount and RBAC live outside the Graph | Medium | `pkg/graph/identity.go` `IdentityProvisioner` |
| [G6](#g6-spec-changes-are-handled-by-delete-and-recreate) | Spec changes handled by delete-and-recreate (kardinal bug, fixed) | Closed | `pkg/graph/client.go` in-place update |
| [G7](#g7-no-ownerreferences-on-graph-children) | No ownerReferences on Graph children | Low | labels + kro inventory; Bundle owns the Graph only |
| [G8](#g8-logic-still-outside-the-graph) | Logic still outside the Graph (time, CEL gates, git/SCM) | High | reconcilers; see `11-graph-purity-tech-debt.md` |

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
So is `spec.bundleName`, on `bundle.status.phase != "Superseded"`, which stops a
Superseded Bundle's Graph from creating steps (E2E-R20). A node that already exists and
turns Unresolved is neither re-applied nor pruned (`executor/simple.go:318-324`,
`pkg/controller/graph/tracking.go:102-106`), so its object stays as history.

Verified on kind: `uat` was created only after `test` was Verified; `prod` stayed absent
while `require-uat-soak` was not ready.

Downsides: the Graph expresses ordering through an error path; `kubectl get graph` shows
nodes as Unresolved instead of "waiting on X"; a CEL typo that also produces
`index out of bounds` would look like "not ready yet" instead of failing.

**Upstream contribution.** Add `spec.gateReadiness: true` (or a per-node
`dependsOnReady`) to the Graph API and wire it to `executor.Simple.GateReadiness` in
`cmd/controller/graphengine.go`. The executor code and tests already exist.

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

**Upstream contribution.** A per-edge or per-node mode where an ignored dependency counts
as satisfied (`includeWhen` with `propagate: false`, or a `skipWhen` that passes
through), so a skipped node does not remove its dependents.

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
  `injectHealthNodes`). They feed Graph readiness, not the PromotionStep's own `readyWhen`.
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

**Upstream contribution.** Allow `readyWhen` to reference nodes the node already depends
on (its existing DAG edges), which keeps the ordering unambiguous. Alternatively, add
graph-level `readyWhen` on the Graph itself that can read any node.

---

## G4: A missing CRD fails the whole Graph

**Need.** A Pipeline may name a health type (Flux, Argo Rollouts, Flagger) whose CRD is
not installed yet. The rest of the promotion should still run.

**kro today.** Every node's GVK is resolved at compile time. A kind the API server does
not serve fails the whole Graph: `pkg/graphengine/compiler/context.go:265-280`
(`ResolveSchema` and `RESTMapping` errors are returned).

**kardinal workaround.** The translator asks the RESTMapper whether the kind is served and
drops the health ref node if it is not (`pkg/translator/translator.go` `servedKind`,
`WithRESTMapper`). The PromotionStep's Go health adapter still runs, so the step stays in
HealthChecking with a "not found" reason until the kind and object exist.

**Upstream contribution.** An opt-in per-node `optional: true` for ref nodes: an
unresolvable GVK makes the node (and only its dependents) Unresolved instead of failing
the compile.

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
  `kardinal.io/reader-namespaces` annotation. After each Graph create, `Prune` deletes the
  bindings that no Graph in the namespace reads through any more. Bindings are not pruned
  when the last Graph of a namespace goes away without another translation.
- The chart ships the applier and reader ClusterRoles, plus an aggregation ClusterRole
  that gives kro list/watch on kardinal kinds (`chart/kardinal-promoter/templates/graph-rbac.yaml`).

Verified on kind: `kardinal-promoter-graph-reader-default` in `argocd`, impersonated
applies succeed, and there are no RBAC or watch errors in the kro logs.

**Upstream contribution.** Document the identity model for Graph authors, or ship a
helper (a `GraphIdentity` pattern, or a flag that lets kro manage per-Graph ServiceAccounts
from a template). This is more about usability than a missing primitive.

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
hash changed, and `ensureGraphExists` recreated it. Under kro that deletes every
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

**Upstream contribution.** Optional: `spec.childOwnerReference: true` so children point
at the Graph. That would give native `kubectl tree` views and GC. Low priority.

---

## G8: Logic still outside the Graph

These pieces are still Go code in reconcilers. Each is a candidate for a Graph primitive.
The detailed tracker is `docs/design/11-graph-purity-tech-debt.md`.

| Logic | Where | Why not in the Graph | Possible kro primitive |
|-------|-------|----------------------|------------------------|
| Wall-clock time (`schedule.*`, soak) | `ScheduleClock` reconciler; `soakMinutes` in `pkg/reconciler/bundle/reconciler.go` `handleSyncEvidence` | CEL in kro has no `now()` and no time-based requeue | A `now` variable plus a Graph `resyncPeriod`, or a built-in clock node |
| Soak time (`bundle.upstreamSoakMinutes`) | Bundle reconciler writes `status.environments[].soakMinutes` and requeues every minute while Promoting; PolicyGate reconciler takes the minimum over the gated environment's direct upstreams | Same as above; also a cross-node read | Same as above |
| PolicyGate CEL (`bundle.*`, `schedule.*`, `metrics.*`, `upstream.*`) | `pkg/reconciler/policygate` | Needs extension functions and data kro does not have (clock, metrics, upstream soak) | CEL extension hooks or custom function libraries in the Graph |
| Git and SCM steps (clone, kustomize, push, open PR, merge detection) | `pkg/steps`, `pkg/scm`, PRStatus reconciler | Side effects on external systems; kro only applies Kubernetes objects | Out of scope for kro. The PromotionStep CR is the Graph-native boundary |
| Health adapters (HealthChecking to Verified) | `pkg/health/adapter.go` via PromotionStep reconciler | G3 (cross-node `readyWhen`) and G4 (optional kinds) | G3 and G4 fixes |

---

## Notes: constraints we design around

These are not gaps, but the translator has to work around them.

- **Node ID grammar.** IDs must match `^[A-Za-z][A-Za-z0-9]*$` and must not be reserved
  (`pkg/graphengine/compiler/validation.go:28-51`: `apiVersion`, `kind`, `metadata`,
  `namespace`, `spec`, `status`, `graph`, `graphengine`, `kro`, `each`, `item`, `items`,
  `object`, `self`, `this`, `context`, CEL keywords).
  The PromotionStep node ID is the environment name in camelCase (`test`, `uat`,
  `prod`); PRStatus, health and gate nodes carry a prefix. kardinal also uses `bundle` as
  a node ID. **Open kardinal bug:** a Pipeline environment named `bundle`, `status`,
  `graph`, `each` or a CEL keyword produces a Graph that kro rejects. Pipeline validation
  should reject those names, or the builder should prefix step IDs.
- **No `forEach` on ref nodes** (`validation.go:131-133`). Multi-region health cannot fan
  out one ref per region; use a label-selector collection ref with a per-element
  `readyWhen` on `each`.
- **An empty collection is ready** (`pkg/graphengine/runtime/node.go:240-252`). A
  selector ref that matches nothing passes. For multi-region PromotionSteps the builder
  adds `size(x) == N` before `x.all(...)` (`verifiedCond`). For selector health refs this
  is documented as a known limitation (`docs/health-adapters.md`).
- **The Graph is not Ready while any node is not Ready.** A `readyWhen` on a node that
  can never become ready keeps the Graph `Ready=False` forever. This happened with
  `PRStatus` nodes for auto environments, which never open a PR. The builder now adds
  `status.merged == true` only for `pr-review` environments.
- **A Graph cannot be suspended.** The `kro.run/reconcile: suspended` annotation
  (`api/v1alpha1/groupversion_info.go:36-55`) is read only by the instance controller
  (`pkg/controller/instance/controller.go:317`), not the Graph controller. Deleting the
  Graph deletes every managed resource (`pkg/controller/graph/controller.go`
  `reconcileDelete`), and a node whose `includeWhen` turns false is pruned
  (`executor/simple.go:289-296`). To stop a Superseded Bundle's Graph while keeping its
  steps and gates, every step node holds on `bundle.status.phase` through `resolvableWhen`
  (G1).
- **Data-pending classification is by error text** (`runtime/errors.go:43-49`). G1's
  workaround depends on `index out of bounds` staying in that list.
- **Graph `Ready` is a live aggregate.** A gate node's `readyWhen` (`status.ready == true`)
  is re-evaluated on every change, so a gate that turns false after `prod` is Verified
  (a weekend window, a soak threshold raised in place) flips the Graph back to
  `Ready=False` even though nothing is left to do. The PromotionSteps are unaffected.
  Read completion from the Bundle (`Ready` condition, `status.metrics`), not from the Graph.

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
