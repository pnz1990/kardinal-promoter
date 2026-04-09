# Tasks: Graph Integration Layer

**Input**: `.specify/specs/001-graph-integration/spec.md` + `docs/design/01-graph-integration.md`
**Feature Branch**: `001-graph-integration`
**Constitution ref**: `.specify/memory/constitution.md`
**Test command**: `go test ./pkg/graph/... -race`

---

## Phase 1: Setup

**Purpose**: Create package skeleton and Go types matching the Graph CRD schema.
**Checkpoint**: `go build ./pkg/graph/...` succeeds.

- [ ] T001 Create `pkg/graph/` directory and `pkg/graph/types.go` with Apache 2.0 header and `GraphSpec`, `GraphNode`, `GraphStatus` structs — file: `pkg/graph/types.go`
- [ ] T002 [P] Create `pkg/graph/client.go` with Apache 2.0 header, `GraphClient` interface, and `dynamicGraphClient` struct skeleton — file: `pkg/graph/client.go`
- [ ] T003 [P] Create `pkg/graph/testing.go` with Apache 2.0 header and function signatures for `CreateGraph()`, `WaitForNodeCreation()`, `DeleteGraph()` — file: `pkg/graph/testing.go`
- [ ] T004 [P] Create `pkg/graph/builder.go` as a stub (will be implemented in spec 002) — file: `pkg/graph/builder.go`

---

## Phase 2: Tests First

**Purpose**: Write unit tests before implementation. Tests should fail with "not implemented".
**Checkpoint**: `go test ./pkg/graph/... -run TestGraphClient` compiles but fails.

- [ ] T005 Write `TestGraphClientCreate_Success` in `client_test.go`: creates Graph with correct ownerReferences using fake dynamic client — file: `pkg/graph/client_test.go`
- [ ] T006 [P] Write `TestGraphClientCreate_Idempotent`: second Create call returns existing Graph — file: `pkg/graph/client_test.go`
- [ ] T007 [P] Write `TestGraphClientWatch_AcceptedFalseEvent`: watch returns error message when Graph has Accepted=False condition — file: `pkg/graph/client_test.go`
- [ ] T008 [P] Write `TestGraphNaming`: Graph name follows `{pipeline}-{bundle-short-version}` pattern — file: `pkg/graph/client_test.go`

---

## Phase 3: Implementation

**Purpose**: Implement GraphClient methods. Tests must go green.
**Checkpoint**: `go test ./pkg/graph/... -race` passes.

- [ ] T009 Implement `dynamicGraphClient.Create()`: build unstructured object, set ownerReferences, call dynamic.Create(), handle AlreadyExists as idempotent — file: `pkg/graph/client.go`
- [ ] T010 [P] Implement `dynamicGraphClient.Get()`: call dynamic.Get(), convert to `*Graph` — file: `pkg/graph/client.go`
- [ ] T011 [P] Implement `dynamicGraphClient.Watch()`: open watch on Graph GVR, emit `GraphEvent` on status condition changes — file: `pkg/graph/client.go`
- [ ] T012 [P] Implement `dynamicGraphClient.Delete()`: call dynamic.Delete() with propagation policy Background — file: `pkg/graph/client.go`
- [ ] T013 [P] Implement `testing.go` helpers: `CreateGraph()` wraps client.Create(), `WaitForNodeCreation()` polls for child CRD, `DeleteGraph()` wraps client.Delete() — file: `pkg/graph/testing.go`

---

## Phase 4: Validation

**Purpose**: Verify all acceptance scenarios and quality standards.
**Checkpoint**: All tests pass, quality gates green.

- [ ] T014 Verify `go test ./pkg/graph/... -race` passes with zero failures
- [ ] T015 [P] Verify `go vet ./pkg/graph/...` passes with zero findings
- [ ] T016 [P] Verify Apache 2.0 copyright header present on all .go files in pkg/graph/
- [ ] T017 [P] Verify go.mod does NOT contain any kro import
- [ ] T018 Run `/speckit.verify-tasks.run` and confirm no phantom completions
