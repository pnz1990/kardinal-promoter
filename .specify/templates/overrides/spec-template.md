# Feature Specification: [FEATURE NAME]

**Feature Branch**: `[###-feature-name]`
**Created**: [DATE]
**Status**: Draft
**Depends on**: [list of spec IDs this depends on, or "nothing"]
**Design doc**: `docs/design/[corresponding design doc].md`
**Constitution ref**: `.specify/memory/constitution.md`
**Contributes to journey(s)**: [J1/J2/J3/... from docs/aide/definition-of-done.md]

---

## Context

[2-3 sentences: why this feature exists and what problem it solves.
Reference the vision or roadmap stage if relevant.]

**Not in scope here**: [What is explicitly excluded from this spec.]

---

## User Scenarios & Testing

### User Story 1 — [Brief Title] (Priority: P1)

[Describe the user journey. For system components, the "user" is the component
that consumes this feature.]

**Why this priority**: [Value delivered.]

**Independent Test**: [Exact test command and test function name.]

**Acceptance Scenarios**:

1. **Given** [initial state], **When** [action], **Then** [expected outcome]
2. **Given** [initial state], **When** [action], **Then** [expected outcome]

---

### User Story 2 — [Brief Title] (Priority: P2)

**Independent Test**: [Test command.]

**Acceptance Scenarios**:

1. **Given** [state], **When** [action], **Then** [outcome]

---

### Edge Cases

- What happens when [boundary condition]?
- What happens on crash and restart (idempotency)?
- What happens when a dependency is unavailable?

---

## Requirements

- **FR-001**: [Capability using MUST/SHOULD/MAY]
- **FR-002**: [Capability]
- **FR-003**: Every handler/reconciler MUST be idempotent

### Package / Module Structure

```
[language-appropriate package layout for this feature]
```

### Key Interfaces and Types

[List the interfaces or types this feature implements or consumes.]

### Integration Points

- Reads from: [dependencies]
- Writes to: [outputs]
- Calls into: [other modules]

---

## Success Criteria

- **SC-001**: `[test command]` passes with zero failures
- **SC-002**: All acceptance scenarios pass
- **SC-003**: No banned filenames (see AGENTS.md)
- **SC-004**: Copyright header on every new source file
- **SC-005**: [Feature-specific measurable outcome]

---

## Assumptions

- [What must be true for this spec to be implementable]
