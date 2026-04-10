# Feature Specification: [FEATURE NAME]

**Feature Branch**: `[###-feature-name]`
**Created**: [DATE]
**Status**: Draft
**Depends on**: [list of spec IDs this depends on]
**Design doc**: `docs/design/[corresponding design doc].md`
**Constitution ref**: `.specify/memory/constitution.md`
**Contributes to journey(s)**: [J1/J2/J3/J4/J5 from docs/aide/definition-of-done.md]

---

## Context

[2-3 sentences explaining why this feature exists and what problem it solves for kardinal-promoter users or the system. Reference the vision doc if relevant.]

**Not in scope here**: [What is explicitly excluded from this spec.]

---

## User Scenarios & Testing

### User Story 1 — [Brief Title] (Priority: P1)

[Describe the user journey. For system components (reconcilers, adapters), the "user" is the reconciler loop or another component.]

**Why this priority**: [Value delivered and why it is first.]

**Independent Test**: [How this can be validated independently. For Go components: specific test command, e.g., `go test ./pkg/reconciler/promotionstep/... -run TestPendingToPromoting`]

**Acceptance Scenarios**:

1. **Given** [initial CRD state or system state], **When** [action or event], **Then** [expected CRD status change or system behavior]
2. **Given** [initial state], **When** [action], **Then** [expected outcome]

---

### User Story 2 — [Brief Title] (Priority: P2)

[Describe the user journey.]

**Why this priority**: [Explanation.]

**Independent Test**: [Test command or validation method.]

**Acceptance Scenarios**:

1. **Given** [state], **When** [action], **Then** [outcome]

---

### Edge Cases

- What happens when [boundary condition]?
- What happens when the Graph controller is down?
- What happens when the reconciler restarts mid-execution?
- How does the system handle [error scenario]?

---

## Requirements

### Functional Requirements

- **FR-001**: [Specific capability using MUST/SHOULD/MAY]
- **FR-002**: [Specific capability]
- **FR-003**: [Idempotency requirement — every reconciler operation MUST be idempotent]

### Go Package Structure

```
pkg/
  [package-name]/
    [file].go           # [what it does]
    [file]_test.go      # unit tests
```

### Key Interfaces and Types

[List the Go interfaces this feature implements or consumes from `docs/design/[spec].md`]

### Integration Points

- Reads from: [which CRDs or packages this reads]
- Writes to: [which CRDs or packages this writes]
- Calls into: [which other packages]

---

## Success Criteria

- **SC-001**: `go test ./pkg/[package]/... -race` passes
- **SC-002**: All acceptance scenarios pass
- **SC-003**: No new `util.go`, `helpers.go`, or `common.go` files
- **SC-004**: Apache 2.0 copyright header on every new `.go` file
- **SC-005**: [Feature-specific measurable outcome]

---

## Assumptions

- kro Graph controller is available in the cluster
- [Other assumptions specific to this feature]
