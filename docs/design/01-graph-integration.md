# 01: Graph Integration Layer

> Status: Historical (checked against the code on 2026-09-29). This spec predates the move to
> upstream kro and several parts differ from the code. For how kardinal uses kro today, read
> [16-graph-capability-ledger](16-graph-capability-ledger.md). Known differences:
> - `pkg/graph/testing.go` does not exist.
> - Graph conditions are kro upstream's (`Accepted`, `ResourcesConverged`, `Ready`), not the
>   Ready reasons listed here.
> - Node templates are maps (`pkg/graph/types.go`), not `RawExtension`.
> - PromotionStep carries `spec.upstreamStates` and `spec.requiredGates`, not
>   `upstreamVerified`/`upstreamEnvironment`.
> - The Graph name comes from `graphNameFrom` (`pkg/graph/names.go`).
> - Skipped environments are left out of the Graph by the builder, not gated with
>   `includeWhen` (ledger G2).
> - There is no Tier 2 nightly job.
> Depends on: nothing (foundation)
> Blocks: all other specs

## Purpose

This spec defines how kardinal-promoter integrates with kro's Graph primitive. Every other component depends on this. The Graph controller creates and reconciles PromotionStep and PolicyGate CRDs in DAG order. The kardinal-controller generates Graph specs, watches Graph status, and reconciles the child CRDs that Graph creates.

## Graph Primitive Reference

Source: [kubernetes-sigs/kro](https://github.com/kubernetes-sigs/kro) v0.10.0-rc.0, Graph kind
enabled by the `GraphKind` feature gate. Docs: [Graph overview](https://kro.run/next/docs/concepts/graph/overview/).
The pinned version lives in `hack/install-kro.sh`. Gaps between what kardinal needs and what the
Graph provides are tracked in [16-graph-capability-ledger.md](16-graph-capability-ledger.md).

> **History.** Until 2026-09 kardinal targeted the pre-upstream Graph controller fork, which used
> a separate API group, a `propagateWhen` keyword and a `watch:` node shape. None of these exist
> upstream. The sections below describe the upstream semantics.

The Graph CRD (`kro.run/v1alpha1/Graph`) is namespace-scoped. It defines:

- **nodes**: A list of nodes with IDs matching `^[A-Za-z][A-Za-z0-9]*$`. Each node sets exactly one of:
  - **template**: a Kubernetes resource with `${...}` CEL expressions; kro creates and owns it.
  - **ref**: an existing object (`metadata.name`) or collection (`metadata.selector.matchLabels`)
    read into scope; kro does not own it.
- **readyWhen**: Per-node CEL over the node itself only (`each` for collections), wrapped in `${}`.
  Feeds the Graph's `Ready` condition. **On a standalone Graph it does not hold back dependents**
  (ledger G1, G3).
- **includeWhen**: Per-node CEL that conditionally includes the node. A false result also
  excludes every node that depends on it (ledger G2).
- **forEach**: Expands a node into a collection, e.g. `forEach: [{region: "${[...]}"}]`.
- **spec.serviceAccountName**: The identity kro impersonates to apply the Graph's children
  (ledger G5).

**Critical distinction for kardinal-promoter:**
- `readyWhen` = health signal (UI, `kubectl get graph`) — does NOT block downstream
- Resolvability = the gate. A dependent's template references an expression that does not resolve
  until the upstream condition holds, so kro cannot render the dependent before then:
  - upstream: `${["Verified"].filter(x_, up.status.state == "Verified")[0]}`
  - gate: `${[g.metadata.name].filter(x_, g.status.ready == true)[0]}`

PolicyGate blocking uses resolvability gating, not `readyWhen`. See `resolvableWhen` in
`pkg/graph/builder.go`.

## Go Package Structure

```
pkg/
  graph/
    client.go          # Graph CR CRUD operations (create, get, watch, delete)
    builder.go         # Builds a Graph spec from Pipeline + Bundle + PolicyGates
    types.go           # Go types mirroring the Graph CRD spec
    testing.go         # Test helpers (create Graph, wait for node creation)
```

The `graph` package does not import any kro Go module directly. It works with the Graph CRD via the Kubernetes dynamic client (`k8s.io/client-go/dynamic`). This avoids a compile-time dependency on kro controller packages. The Graph CRD schema is defined in `types.go` as Go structs matching the YAML structure.

## Graph CRD Schema (as used by kardinal-promoter)

```go
type GraphSpec struct {
    Nodes              []GraphNode `json:"nodes,omitempty"`
    ServiceAccountName string      `json:"serviceAccountName,omitempty"` // default kardinal-graph
}

type GraphNode struct {
    ID          string                 `json:"id"`
    Template    map[string]interface{} `json:"template,omitempty"` // exactly one of
    Ref         map[string]interface{} `json:"ref,omitempty"`      // template / ref
    ReadyWhen   []string               `json:"readyWhen,omitempty"`   // self-only health signal
    IncludeWhen []string               `json:"includeWhen,omitempty"`
    ForEach     []map[string]string    `json:"forEach,omitempty"`     // [{iterator: "${list}"}]
}

// Gating dependents:
//
//   ReadyWhen does NOT block downstream nodes on a standalone Graph (ledger G1).
//   The builder instead embeds a resolvability expression in the dependent's
//   template that only resolves once the upstream condition holds:
//
//   PromotionStep upstream: ${["Verified"].filter(x_, dev.status.state == "Verified")[0]}
//   PolicyGate upstream:    ${[noWeekendDeploys.metadata.name].filter(x_, noWeekendDeploys.status.ready == true)[0]}
//
// ReadyWhen on PolicyGate nodes is only the UI health signal (shows pass/fail colour).

type GraphStatus struct {
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    // Accepted: the Graph spec is valid (CEL compiles, DAG is acyclic)
    // Ready: rollup of node plan states
    //   True/Ready: all nodes converged
    //   Unknown/Pending: waiting for upstream data
    //   Unknown/NotReady: applied but readyWhen not satisfied (health signal)
    //   False/Error: client request failed (4xx)
    //   False/Conflict: SSA field ownership contested
}
```

The `template` field is a `runtime.RawExtension` containing the full Kubernetes resource YAML for the node. For kardinal-promoter, this is always a PromotionStep or PolicyGate CRD.

## Creating a Graph

The kardinal-controller creates a Graph CR using the dynamic client:

```go
func (c *GraphClient) Create(ctx context.Context, graph *Graph) error {
    // GraphGVR is defined in pkg/graph/types.go (kro.run/v1alpha1/graphs)
    unstructured := toUnstructured(graph)
    _, err := c.dynamic.Resource(GraphGVR).Namespace(graph.Namespace).Create(ctx, unstructured, metav1.CreateOptions{})
    return err
}
```

The Graph CR is owned by the Bundle CR via `ownerReferences`:

```go
graph.OwnerReferences = []metav1.OwnerReference{
    {
        APIVersion: "kardinal.io/v1alpha1",
        Kind:       "Bundle",
        Name:       bundle.Name,
        UID:        bundle.UID,
        Controller: ptr.To(true),
    },
}
```

Deleting a Bundle cascades to the Graph, which cascades to all PromotionStep and PolicyGate CRs that Graph created.

## Watching Graph Status

The kardinal-controller watches Graph CRs to detect when the overall promotion is complete or has failed:

```go
// Watch for Graph status changes
informer := dynamicInformer.ForResource(graphGVR)
informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
    UpdateFunc: func(old, new interface{}) {
        graph := fromUnstructured(new)
        if graphIsReady(graph) {
            // All environments verified, mark Bundle as Verified
        }
        if graphIsFailed(graph) {
            // A step failed. Rollback is a new Bundle, not a Graph change.
        }
    },
})
```

Graph status conditions:
- `Accepted=True`: the Graph spec is valid, nodes are being created.
- `Ready=True`: all included nodes have their `readyWhen` satisfied (health signal rollup).
- `Ready=Unknown` with reason `NotReady` or `Pending`: nodes are converging.
- `Ready=False` with reason `Error` or `Conflict`: nodes require operator action.
- `Accepted=False`: the Graph spec has errors (invalid CEL, circular dependency).

## Dependency Edge Creation

Graph infers edges from CEL `${...}` references. To create an edge from node A to node B, B's template must contain a reference to A.

kardinal-promoter creates edges using fields that the reconcilers consume:

| Source node | Target node | Reference field in target | Purpose of the field |
|---|---|---|---|
| dev (PromotionStep) | staging (PromotionStep) | `spec.upstreamVerified: ${dev.status.state}` | PromotionStep reconciler checks upstream is Verified before proceeding |
| staging (PromotionStep) | noWeekendDeploys (PolicyGate) | `spec.upstreamEnvironment: ${staging.status.state}` | PolicyGate reconciler knows which environment to check soak time against |
| staging (PromotionStep) | stagingSoak (PolicyGate) | `spec.upstreamEnvironment: ${staging.status.state}` | Same as above |
| noWeekendDeploys (PolicyGate) | prod (PromotionStep) | `spec.requiredGates: ["${noWeekendDeploys.metadata.name}", "${stagingSoak.metadata.name}"]` | PromotionStep reconciler knows which gates must pass |

These fields are not synthetic placeholders. They carry data that the reconcilers need AND they create the CEL references that Graph uses for dependency inference.

Proposed contribution to Graph: Add optional `dependsOn` to Graph node spec for cases where dependencies are structural rather than data-driven. Until this is available, all edges use field references.

## Graph Naming Convention

Graphs are named `{pipeline}-{bundle-short-version}`. Example: `my-app-v1-29-0`.

The name is derived from the Pipeline name and the Bundle's semver tag (or commit SHA prefix for config Bundles). Collisions are prevented by including a timestamp suffix when needed: `my-app-v1-29-0-1712567890`.

## Testing Strategy

### Unit Tests

The `graph/builder.go` module is tested by constructing Graph specs from test Pipeline, Bundle, and PolicyGate inputs and asserting:
- Correct number of nodes
- Correct dependency edges (CEL references present)
- PolicyGate nodes injected in the right position
- `readyWhen` expressions are correct
- `includeWhen` correctly handles `intent.skipEnvironments`
- `intent.targetEnvironment` limits which nodes are included

### Integration Tests

Integration tests require a running Graph controller. The test harness:

1. Starts a local Kubernetes cluster (envtest or kind).
2. Installs the Graph CRD and starts the Graph controller.
3. Creates a Graph CR with test PromotionStep and PolicyGate templates.
4. Verifies the Graph controller creates the child CRDs in the correct order.
5. Updates a child CRD's status to satisfy `readyWhen` and verifies the next child is created.

These tests validate that the Graph controller behaves as expected and that the dependency inference from CEL references works correctly.

### Compatibility Testing

The Graph API is experimental. To detect breaking changes:
- Pin the Graph CRD version in the Helm chart.
- Run integration tests against the pinned version in CI (Tier 1).
- Run integration tests against the latest Graph controller nightly (Tier 2).
- If the nightly test fails, the breaking change is detected before it affects users.

## Error Handling

| Error | Behavior |
|---|---|
| Graph CRD not installed | The Bundle and graph cleanup controllers watch Graphs, so their caches cannot sync. About 2 minutes after the controller becomes leader, it exits, and the pod restarts until kro is installed. `kardinal doctor` reports the missing CRD. |
| Graph controller not running | The Graph is created, but no child objects appear and the promotion stalls. There is no timeout. `kardinal doctor` checks that the kro pod runs. |
| Invalid Graph spec (Accepted=False) | The Graph status condition `Accepted=False` is set with an error message. The controller copies it to the Bundle as `GraphAccepted` and marks the Bundle Failed with reason `GraphRejected`. |
| Graph deletion (Bundle GC) | When a Bundle is deleted (`historyLimit` GC or by hand), Kubernetes deletes its Graph through the owner reference. kro's finalizer then deletes the Graph's children, which carry no ownerReferences (ledger G7). |

## What This Spec Does NOT Cover

- How to build Graph specs from Pipeline CRDs (see 02-pipeline-to-graph-translator)
- How PromotionStep CRs are reconciled (see 03-promotionstep-reconciler)
- How PolicyGate CRs are reconciled (see 04-policygate-reconciler)
- The Graph controller's internal implementation (maintained by the kro team)

## Present

✅ (fork era, superseded by kro v0.10.0-rc.0) Graph controller fork pinned to `cdc4bb9` (2026-04-17): schema-aware CEL, forEach data-loss fix,
   NodeTypeOwn→NodeTypeTemplate cosmetic rename (PR merged before #789 tracking)

✅ (fork era, superseded by kro v0.10.0-rc.0) Graph controller fork upgraded to `3376810` (2026-04-18, PR #789): propagation trigger on self-state
   refresh — fixes J1 blocker where UAT PromotionStep never started because the fork's
   Path 2 dispatch did not mark dependents as `propagationTriggered`.
   Root cause: fork commit `3bcbe92` (correctness: propagation trigger on self-state
   refresh + cycle error format).

✅ ensurePipelineSpecCurrent empty-hash guard (2026-04-18, PR #789): Bundles promoted before
   PipelineSpecHash was introduced (pre-#634) had an empty stored hash. The guard now treats
   an empty stored hash as "uninitialised" — saves it and returns without deleting the Graph.
   Previously the empty hash was misinterpreted as "spec has changed", causing a spurious
   Graph deletion that reset all PromotionSteps to empty status.

✅ (fork era, superseded by kro v0.10.0-rc.0) Graph controller fork upgraded to `d6cbc54` (2026-04-19, PR #803): 5 additive commits — forEach
   incremental O(K) diff, WatchManager canonical Kind caching fix, context-aware hashing.
   No breaking changes to kardinal integration. No source changes required.

✅ Graph controller upgrade cadence: the kro pin lives in `hack/install-kro.sh`. AGENTS.md
   §kro Upgrade Cadence describes the upgrade check. The agent loop that ran it was retired
   (#1343).

✅ Migrated to upstream kro v0.10.0-rc.0 Graph (`kro.run/v1alpha1`, `GraphKind` gate, 2026-09):
   resolvability gating replaces `propagateWhen`, `ref` nodes replace `watch:`, per-Graph
   `spec.serviceAccountName` identity provisioned by the controller. kro is a separate install.
   Remaining gaps: [16-graph-capability-ledger.md](16-graph-capability-ledger.md).

## Future

_(no items)_
