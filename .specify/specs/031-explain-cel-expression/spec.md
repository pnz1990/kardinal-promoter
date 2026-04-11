# Feature Specification: Show CEL Expression + Current Value in kardinal explain

**Feature Branch**: `031-explain-cel-expression`
**Created**: 2026-04-11
**Status**: Draft
**Depends on**: 010-policygate-cel, 013-promotionstep-reconciler
**Contributes to journey(s)**: J3, J5
**GitHub issue**: #117

---

## Context

The DoD J3 requires: "kardinal explain shows CEL expression, current value, and result."
Currently the explain output does not show the expression text or evaluated value.

---

## User Scenarios

### SC-001: CEL expression shown in explain

**Given** a `no-weekend-deploys` PolicyGate with expression `!schedule.isWeekend`,
**When** the user runs `kardinal explain nginx-demo --env prod`,
**Then** the output includes the expression `!schedule.isWeekend` and the evaluated value (e.g. `isWeekend=false`).

---

## Functional Requirements

- **FR-001** MUST show `spec.expression` for each PolicyGate in explain output
- **FR-002** MUST show evaluated value from `status.message` or a parsed representation
- **FR-003** MUST show PASS/BLOCK/PENDING result per gate

---

## Success Criteria

- **SC-001**: Test: explain output for a PolicyGate includes the CEL expression text
- **SC-002**: Test: when gate is not yet evaluated, VALUE shows `-`
