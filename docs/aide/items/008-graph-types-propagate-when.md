# Item 008: Add PropagateWhen to GraphNode type

> **Queue**: queue-004 (pre-queue blocking fix, before Stage 3 items)
> **Branch**: `008-graph-types-propagate-when`
> **Depends on**: 007 (merged — Stage 2 complete)
> **Dependency mode**: merged
> **Assignable**: immediately
> **Contributes to**: All journeys (GraphNode type correctness is foundational for Stage 3)
> **Priority**: BLOCKING — Stage 3 items must not be assigned until this is merged

---

## Goal

`pkg/graph/types.go` was implemented in Stage 2 (PR #23) without the `PropagateWhen`
field on `GraphNode`. This field is required for PolicyGate blocking:

- `ReadyWhen` = health signal only (UI, `kubectl get graph`). Does **not** block downstream.
- `PropagateWhen` = data-flow gate. When unsatisfied, downstream nodes are not re-evaluated.
  **This is what causes PolicyGates to block PromotionSteps.**

Without `PropagateWhen` in the Go type, the Stage 3 `graph.Builder` cannot set it on
PolicyGate nodes — gates would be silently bypassed at runtime.

**Source**: `docs/design/design-v2.1.md` §3.5; `docs/design/01-graph-integration.md`
(updated in PR #25 to include this field).

---

## Deliverables

### 1. Add `PropagateWhen` to `GraphNode` in `pkg/graph/types.go`

```go
// GraphNode represents one resource node in the kro Graph.
type GraphNode struct {
    // ID is the unique node identifier within the Graph.
    ID string `json:"id"`

    // Template is the raw resource template for this node.
    Template map[string]interface{} `json:"template,omitempty"`

    // ReadyWhen holds CEL expressions that are a HEALTH SIGNAL ONLY.
    // They feed the Graph's aggregated Ready condition and the UI.
    // They do NOT block downstream node execution.
    ReadyWhen []string `json:"readyWhen,omitempty"`

    // PropagateWhen holds CEL expressions that GATE DATA FLOW to dependents.
    // When any expression is unsatisfied, downstream nodes do not receive
    // updated data and are not re-evaluated. This is the correct mechanism
    // for PolicyGate blocking. See design-v2.1.md §3.5.
    //
    // For PromotionStep nodes:
    //   propagateWhen: ["${dev.status.state == \"Verified\"}"]
    //
    // For PolicyGate nodes:
    //   propagateWhen: ["${noWeekendDeploys.status.ready == true}"]
    PropagateWhen []string `json:"propagateWhen,omitempty"`

    // IncludeWhen holds CEL expressions that conditionally include this node.
    IncludeWhen []string `json:"includeWhen,omitempty"`

    // ForEach is a CEL expression that stamps out one node per collection item.
    ForEach string `json:"forEach,omitempty"`
}
```

### 2. Update `DeepCopyInto` for `GraphNode` in `pkg/graph/types.go`

If a `DeepCopy` or `DeepCopyInto` method exists for `GraphNode`, add copy logic for
the new `PropagateWhen` slice (same pattern as `ReadyWhen` and `IncludeWhen`):

```go
if in.PropagateWhen != nil {
    in, out := &in.PropagateWhen, &out.PropagateWhen
    *out = make([]string, len(*in))
    copy(*out, *in)
}
```

If no DeepCopy exists, no action needed (it will be auto-generated or not needed).

### 3. Add unit test in `pkg/graph/` verifying `PropagateWhen` marshals correctly

In the existing graph package tests, add one test case that:
1. Constructs a `GraphNode` with `PropagateWhen: []string{"${gate.status.ready == true}"}`.
2. Marshals it to JSON.
3. Unmarshals back.
4. Asserts the `PropagateWhen` field round-trips correctly.

```go
func TestGraphNodePropagateWhenRoundtrip(t *testing.T) {
    node := GraphNode{
        ID:            "no-weekend-deploys",
        PropagateWhen: []string{`${noWeekendDeploys.status.ready == true}`},
    }
    data, err := json.Marshal(node)
    require.NoError(t, err)
    assert.Contains(t, string(data), "propagateWhen")

    var got GraphNode
    require.NoError(t, json.Unmarshal(data, &got))
    assert.Equal(t, node.PropagateWhen, got.PropagateWhen)
}
```

---

## Acceptance Criteria

- [ ] `PropagateWhen []string` field present in `GraphNode` with json tag `propagateWhen,omitempty`
- [ ] `go build ./...` passes (no compile errors)
- [ ] `go test ./pkg/graph/... -race` passes (PropagateWhen roundtrip test green)
- [ ] `go vet ./...` passes with no new warnings
- [ ] Copyright header `// Copyright 2026 The kardinal-promoter Authors.` on any modified file

---

## Anti-patterns to Avoid

- Do NOT add `util.go`, `helpers.go`, or `common.go`
- Do NOT add `kro` to go.mod (use dynamic client as designed)
- Use `testify/assert` and `testify/require` — not standard `testing.T` assertions

---

## Notes for Engineer

This is a small, focused fix. The entire change is adding one field to `GraphNode` in
`pkg/graph/types.go`, updating DeepCopy if present, and adding one roundtrip test.
No business logic changes. No other files need modification.

After merging, the Stage 3 `graph.Builder` can set `PropagateWhen` on PolicyGate nodes.
See PR #25 (`docs/design/01-graph-integration.md`) for the updated `GraphNode` struct
definition showing the correct field with usage examples.
