# Feature Specification: Pipeline-to-Graph Translator

**Feature Branch**: `002-pipeline-translator`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 001-graph-integration
**Design doc**: `docs/design/02-pipeline-to-graph-translator.md`
**Contributes to journey(s)**: J1, J2, J3, J4 (Pipeline translation is core to all promotions)
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

The translator reads a Pipeline CRD + Bundle CRD + PolicyGate CRDs and produces a kro Graph spec. It is the bridge between the user-facing Pipeline abstraction and the Graph execution engine. One Graph is generated per Bundle, tailored to the Bundle's intent.

**Not in scope**: Reconciling PromotionStep or PolicyGate CRDs. Creating or deleting Graph CRs (spec 001).

---

## User Scenarios & Testing

### User Story 1 — Linear pipeline translates to a linear Graph (Priority: P1)

A Pipeline with three environments `[dev, staging, prod]` produces a Graph with three PromotionStep nodes in sequential dependency order.

**Independent Test**: `go test ./pkg/translator/... -run TestLinearPipeline`

**Acceptance Scenarios**:

1. **Given** a Pipeline `[dev, staging, prod]` with no `dependsOn`, **When** `Translate()` is called, **Then** the Graph has 3 PromotionStep nodes: staging depends on dev, prod depends on staging
2. **Given** `intent.target: staging`, **When** `Translate()` is called, **Then** the Graph only includes dev and staging nodes (prod excluded)
3. **Given** two environments with `dependsOn: [staging]`, **When** `Translate()` is called, **Then** both depend on staging and Graph can execute them in parallel

---

### User Story 2 — PolicyGate injection (Priority: P1)

Org-level PolicyGates are injected between the upstream environment and the gated environment as mandatory DAG dependencies.

**Independent Test**: `go test ./pkg/translator/... -run TestPolicyGateInjection`

**Acceptance Scenarios**:

1. **Given** 2 org PolicyGates with `applies-to: prod`, **When** `Translate()` is called, **Then** the Graph has 2 PolicyGate nodes between staging and prod, and prod depends on both
2. **Given** a team-level PolicyGate with `applies-to: prod`, **When** `Translate()` is called, **Then** it is injected alongside org gates
3. **Given** no PolicyGates exist, **When** `Translate()` is called, **Then** no PolicyGate nodes are in the Graph

---

### User Story 3 — Skip-permission validation (Priority: P2)

`intent.skip` listing an env with org gates is denied unless a SkipPermission gate permits it.

**Independent Test**: `go test ./pkg/translator/... -run TestSkipPermission`

**Acceptance Scenarios**:

1. **Given** `intent.skip: [staging]` and an org gate `applies-to: staging`, and no SkipPermission gate, **When** `Translate()` is called, **Then** it returns a `SkipDenied` error
2. **Given** `intent.skip: [staging]`, an org gate, and a SkipPermission gate with `bundle.labels.hotfix == true`, and the Bundle has `labels.hotfix: "true"`, **When** `Translate()`, **Then** staging is excluded and the Graph is valid

---

### Edge Cases

- Empty environments list: returns error
- Circular `dependsOn`: returns error
- `intent.target` names unknown environment: returns error
- Two PolicyGates same name in different namespaces: both injected, node IDs include namespace

---

## Requirements

- **FR-001**: `Translate(pipeline, bundle, policyGates)` → `(GraphSpec, error)`
- **FR-002**: Default ordering: each env depends on the previous
- **FR-003**: `dependsOn` overrides sequential default
- **FR-004**: Org gates injected as mandatory nodes between upstream env and gated env
- **FR-005**: `intent.target` limits nodes; `intent.skip` removes nodes after permission check
- **FR-006**: Graph names: `{pipeline.name}-{bundle.shortVersion}`
- **FR-007**: Graph owned by Bundle via ownerReferences

### Go Package Structure

```
pkg/translator/
  translator.go       # Translate() entrypoint
  environment.go      # Ordering and dependsOn resolution
  gates.go            # PolicyGate collection and matching
  skip.go             # Skip-permission validation
  graph.go            # Graph spec assembly
  translator_test.go  # Unit tests (no cluster required)
```

---

## Success Criteria

- **SC-001**: `go test ./pkg/translator/... -race` passes
- **SC-002**: All 11 unit test cases from design doc pass
- **SC-003**: Apache 2.0 header on every .go file
