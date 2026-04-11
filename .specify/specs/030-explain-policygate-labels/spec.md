# Feature Specification: Fix kardinal explain Zero PolicyGates

**Feature Branch**: `030-explain-policygate-labels`
**Created**: 2026-04-11
**Status**: Draft
**Depends on**: 013-promotionstep-reconciler
**Contributes to journey(s)**: J1, J3, J5
**GitHub issue**: #116

---

## Context

`kardinal explain` queries for PolicyGate nodes using a label key that doesn't match
what the Graph builder sets on those nodes. Result: zero gates shown.

---

## User Scenarios

### SC-001: PolicyGates visible in explain

**Given** a Pipeline with a `no-weekend-deploys` PolicyGate and an active Bundle promoting to prod,
**When** the user runs `kardinal explain nginx-demo --env prod`,
**Then** the output lists the PolicyGate node with name `no-weekend-deploys` and its current state.

---

## Functional Requirements

- **FR-001** MUST use the same label key in the explain query as the Graph builder sets on nodes
- **FR-002** When PolicyGates exist, MUST list them in the explain output (not zero)

---

## Success Criteria

- **SC-001**: Test: explain with mocked PromotionStep + PolicyGate returns non-empty gate list
