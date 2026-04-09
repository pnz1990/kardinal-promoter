# Feature Specification: Graph Integration Layer

**Feature Branch**: `001-graph-integration`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: nothing (foundation)
**Design doc**: `docs/design/01-graph-integration.md`
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

kardinal-promoter uses kro's Graph primitive (`kro.run/v1alpha1/Graph`) as its DAG execution engine. This spec defines `pkg/graph/` — the Go package that creates, watches, and deletes Graph CRs via the Kubernetes dynamic client. No compile-time kro dependency is introduced.

**Not in scope**: Building Graph specs from Pipeline inputs (spec 002). Reconciling PromotionStep/PolicyGate CRDs (specs 003, 004).

---

## User Scenarios & Testing

### User Story 1 — Create a Graph and observe child CRDs (Priority: P1)

A controller creates a Graph CR with PromotionStep and PolicyGate node templates. The Graph controller creates child CRDs in dependency order.

**Why this priority**: Every other spec depends on this. Nothing works without it.

**Independent Test**: `go test ./pkg/graph/... -tags integration -run TestCreateGraphAndObserveChildren`

**Acceptance Scenarios**:

1. **Given** a Graph CR with one PromotionStep node, **When** the Graph controller reconciles it, **Then** a PromotionStep CR is created within 5 seconds
2. **Given** node B's template references `${A.status.state}`, **When** node A satisfies its `readyWhen`, **Then** Graph creates node B
3. **Given** a Bundle owns a Graph via `ownerReferences`, **When** the Bundle is deleted, **Then** the Graph and all child CRDs cascade-delete

---

### User Story 2 — GraphClient CRUD (Priority: P1)

`pkg/graph/client.go` provides Create, Get, Watch, Delete for Graph CRs using only the dynamic client.

**Independent Test**: `go test ./pkg/graph/... -run TestGraphClient`

**Acceptance Scenarios**:

1. **Given** a valid Graph spec, **When** `client.Create()` is called, **Then** the Graph CR is created with ownerReferences pointing to the Bundle
2. **Given** an existing Graph, **When** `client.Create()` is called again, **Then** it returns the existing Graph (idempotent)
3. **Given** a Graph with `Accepted=False`, **When** watching, **Then** the error message is surfaced in the watch event

---

### Edge Cases

- Graph controller not running: `Create()` succeeds but children never appear — caller must timeout and mark Bundle Failed
- Graph CRD not installed: `Create()` returns an error — controller logs clearly, does not crash
- Graph name collision: Create is idempotent

---

## Requirements

- **FR-001**: `pkg/graph/` MUST use only `k8s.io/client-go/dynamic` — no kro import
- **FR-002**: `types.go` MUST define `GraphSpec`, `GraphNode`, `GraphStatus` structs
- **FR-003**: `Create()` MUST set ownerReferences with Bundle as owner
- **FR-004**: `Create()` MUST be idempotent
- **FR-005**: `testing.go` MUST provide `CreateGraph()`, `WaitForNodeCreation()`, `DeleteGraph()`
- **FR-006**: Graph CR names MUST be `{pipeline}-{bundle-short-version}`

### Go Package Structure

```
pkg/graph/
  client.go       # GraphClient: Create/Get/Watch/Delete
  builder.go      # stub — spec 002
  types.go        # GraphSpec, GraphNode, GraphStatus
  testing.go      # integration test helpers
  client_test.go  # unit tests with fake dynamic client
```

---

## Success Criteria

- **SC-001**: `go test ./pkg/graph/... -race` passes
- **SC-002**: `go vet ./pkg/graph/...` zero findings
- **SC-003**: Apache 2.0 header on every .go file
- **SC-004**: No kro import in go.mod
