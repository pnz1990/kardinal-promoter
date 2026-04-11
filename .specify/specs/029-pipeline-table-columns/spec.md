# Feature Specification: Fix FormatPipelineTable Per-Environment Columns

**Feature Branch**: `029-pipeline-table-columns`
**Created**: 2026-04-11
**Status**: Draft
**Depends on**: 011-cli-foundation, 013-promotionstep-reconciler
**Contributes to journey(s)**: J1, J5
**GitHub issue**: #115

---

## Context

`kardinal get pipelines` shows a generic PHASE column instead of per-environment status columns.
Workshop 1 requires per-environment columns matching the definition-of-done format.

---

## User Scenarios

### SC-001: Per-environment columns in pipeline table

**Given** a Pipeline `nginx-demo` with environments `test`, `uat`, `prod`
and a Bundle with tag `v1.29.0` promoting through them,
**When** the user runs `kardinal get pipelines`,
**Then** the output table has columns: `PIPELINE BUNDLE TEST UAT PROD AGE`
with the current phase of each environment shown under its column.

### SC-002: Dynamic columns per pipeline

**Given** multiple pipelines with different environment names,
**When** the user runs `kardinal get pipelines`,
**Then** each pipeline row shows its environments' status under correctly labeled columns
(absent environments show `-`).

---

## Functional Requirements

- **FR-001** MUST show PIPELINE, BUNDLE, one column per environment in spec order, AGE
- **FR-002** Environment column value MUST be the PromotionStep state for that environment (Pending/Promoting/Verified/Failed/Paused or `-`)
- **FR-003** BUNDLE column MUST show the active bundle version tag (or `-` if no active bundle)
- **FR-004** When multiple pipelines use different environments, columns MUST be the union of all environment names aligned across all rows

---

## Success Criteria

- **SC-001**: Test: `FormatPipelineTable` with a 3-env pipeline returns correct column headers
- **SC-002**: Test: multi-pipeline output uses union columns with `-` for absent environments
