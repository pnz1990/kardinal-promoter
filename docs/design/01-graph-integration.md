# 01: Graph Integration Layer

> Status: Comprehensive
> Depends on: nothing (foundation)
> Blocks: all other specs

## Purpose

This spec defines how kardinal-promoter integrates with kro's Graph primitive. Every other component depends on this. The Graph controller creates and reconciles PromotionStep and PolicyGate CRDs in DAG order. The kardinal-controller generates Graph specs, watches Graph status, and reconciles the child CRDs that Graph creates.

## Graph Primitive Reference

Source: [ellistarn/kro/tree/krocodile](https://github.com/ellistarn/kro/tree/krocodile/experimental)

> **Important — track this branch actively.** krocodile/experimental is under rapid development
> (20+ commits/day as of 2026-04-09). Before implementing any Graph integration, read the current
> design docs and git log. API and semantics are changing. See Section 17 in design-v2.1.md for
> the contribution and tracking policy.

The Graph CRD (`experimental.kro.run/v1alpha1/Graph`) is namespace-scoped. It defines:

> **API group update (krocodile commit `48224264`, 2026-04-10)**: Graph CRD moved from `kro.run`
> to `experimental.kro.run` to eliminate CRD conflicts with upstream kro. All GVK/GVR references
> in this project use `experimental.kro.run`. The `GraphGVK` and `GraphGVR` constants in
> `pkg/graph/types.go` are authoritative.


- **nodes**: A list of resource templates with IDs. Each node has a Kubernetes resource template with `${...}` CEL expressions.
- **readyWhen**: Per-node CEL expressions that are a **health signal only**. They feed the Graph's aggregated `Ready` condition and the `.ready()` function. **They do NOT gate downstream execution.**
- **propagateWhen**: Per-node CEL expressions that **gate data flow to dependents**. When unsatisfied, dependents retain their previous state and are not re-evaluated. **This is what blocks downstream nodes.**
- **includeWhen**: Per-node CEL expressions that conditionally include or exclude a node from the DAG.
- **forEach**: Stamp out one node per item in a collection.
- **finalizes**: Teardown hooks executed in reverse dependency order during Graph deletion.

**Critical distinction for kardinal-promoter:**
- `readyWhen` = health signal (UI, `kubectl get graph`) — does NOT block downstream
- `propagateWhen` = data-flow gate — DOES block downstream when unsatisfied

PolicyGate blocking uses `propagateWhen`, not `readyWhen`. See design-v2.1.md Section 3.5.

## Go Package Structure

```
pkg/
  graph/
    client.go          # Graph CR CRUD operations (create, get, watch, delete)
    builder.go         # Builds a Graph spec from Pipeline + Bundle + PolicyGates
    types.go           # Go types mirroring the Graph CRD spec
    testing.go         # Test helpers (create Graph, wait for node creation)
```

The `graph` package does not import any kro Go module directly. It works with the Graph CRD via the Kubernetes dynamic client (`k8s.io/client-go/dynamic`). This avoids a compile-time dependency on the experimental kro codebase, which may change. The Graph CRD schema is defined in `types.go` as Go structs matching the YAML structure.

## Graph CRD Schema (as used by kardinal-promoter)

```go
type GraphSpec struct {
    Nodes []GraphNode `json:"nodes"`
}

type GraphNode struct {
    ID            string               `json:"id"`
    Template      runtime.RawExtension `json:"template"`
    ReadyWhen     []string             `json:"readyWhen,omitempty"`
    PropagateWhen []string             `json:"propagateWhen,omitempty"`
    IncludeWhen   []string             `json:"includeWhen,omitempty"`
    ForEach       string               `json:"forEach,omitempty"`
}

// PropagateWhen usage:
//
//   ReadyWhen   = health signal only. Feeds the Graph's aggregated Ready condition
//                 and the UI. Does NOT block downstream nodes.
//   PropagateWhen = data-flow gate. When unsatisfied, downstream nodes do not receive
//                   updated data and are not re-evaluated. This is the field that
//                   gates PolicyGate blocking. See design-v2.1.md §3.5.
//
// For PromotionStep nodes: use PropagateWhen to block downstream when not Verified.
//   propagateWhen: ["${dev.status.state == \"Verified\"}"]
//
// For PolicyGate nodes: use PropagateWhen to block downstream when gate not ready.
//   propagateWhen: ["${noWeekendDeploys.status.ready == true}"]
//
// ReadyWhen on PolicyGate nodes is only the UI health signal (shows pass/fail colour).
// The actual blocking is done by PropagateWhen on the upstream PolicyGate node.

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
    // GraphGVR is defined in pkg/graph/types.go (experimental.kro.run/v1alpha1/graphs)
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
            // A step failed, trigger rollback
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
| Graph CRD not installed | Controller logs an error on startup and exits. Graph is a prerequisite. |
| Graph controller not running | Graph CR is created but child CRDs are never produced. The promotion stalls. The controller detects this via a timeout (configurable, default 5 minutes) and marks the Bundle as Failed with reason "Graph controller not responding." |
| Invalid Graph spec (Accepted=False) | The Graph status condition `Accepted=False` is set with an error message. The controller reads this, marks the Bundle as Failed with the error message, and logs it. |
| Graph deletion (Bundle GC) | When a Bundle is garbage-collected, the owned Graph and all its child CRDs are cascade-deleted by Kubernetes. No controller intervention needed. |

## What This Spec Does NOT Cover

- How to build Graph specs from Pipeline CRDs (see 02-pipeline-to-graph-translator)
- How PromotionStep CRs are reconciled (see 03-promotionstep-reconciler)
- How PolicyGate CRs are reconciled (see 04-policygate-reconciler)
- The Graph controller's internal implementation (maintained by the kro team)
