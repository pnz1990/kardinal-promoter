# Spec: kardinal override — Emergency PolicyGate Override with Audit Record (K-09)

> Feature ID: 403-kardinal-override
> Issue: #451
> Milestone: v0.6.0 — Pipeline Expressiveness
> Status: Implemented

## Background

Emergency escape hatches happen. When a deployment is critical (P0 hotfix, incident response)
and a PolicyGate is blocking, operators need a way to bypass it — with a mandatory audit trail.

## Functional Requirements

- FR-001: `kardinal override <pipeline> --gate <name> --reason <text>` CLI command
- FR-002: Override patches `PolicyGate.spec.overrides[]` with a `PolicyGateOverride` record
- FR-003: Override is time-limited: `--expires-in` flag (default: 1h)
- FR-004: `PolicyGateOverride` contains: reason (required), stage (optional, empty=all), expiresAt, createdBy, createdAt
- FR-005: PolicyGate reconciler checks for active (non-expired) overrides before CEL evaluation
- FR-006: Active override → gate passes immediately (status.ready=true) with OVERRIDDEN reason
- FR-007: Expired overrides are NEVER deleted — they are an immutable audit trail
- FR-008: `--stage` flag scopes override to a specific environment (empty = all stages)

## Acceptance Criteria

- AC-001: Override command patches PolicyGate with required fields
- AC-002: Invalid --expires-in duration returns error
- AC-003: Gate not found returns error
- AC-004: Multiple overrides coexist; most recent non-expired one wins
- AC-005: Empty stage override applies to any environment
- AC-006: Reconciler passes gate when active override exists

## Architecture

✅ Graph-first: CLI patches PolicyGate.spec (CRD mutation). PolicyGate reconciler
reads spec.overrides and writes status.ready. Graph reads status.ready. No logic
outside the reconciler.
