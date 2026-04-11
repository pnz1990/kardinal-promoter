# Item 029: Fix FormatPipelineTable Per-Environment Status Columns

> **Stage**: Workshop 1 Parity (CLI fix)
> **Queue**: queue-014
> **Priority**: critical
> **Size**: s
> **Depends on**: 011 (CLI foundation), 013 (PromotionStep reconciler)
> **dependency_mode**: merged
> **GitHub issue**: #115

## Context

`kardinal get pipelines` currently outputs a generic PHASE column:
```
PIPELINE         PHASE       ENVIRONMENTS   PAUSED   AGE
nginx-demo       Promoting   3              false    2m
```

The Workshop 1 definition-of-done requires per-environment columns:
```
PIPELINE    BUNDLE    TEST       UAT     PROD     AGE
nginx-demo  v1.29.0  Verified   ...     ...      2m
```

## Acceptance Criteria

- `kardinal get pipelines` output format:
  - Column 1: PIPELINE (pipeline name)
  - Column 2: BUNDLE (active bundle version tag, or `-` if none)
  - Columns 3..N: one column per environment in Pipeline.spec.environments order, showing state (Pending/Promoting/Verified/Failed/Paused or `-`)
  - Last column: AGE
- Works with any number of environments (dynamic columns based on pipeline spec)
- When multiple pipelines exist, all rows aligned to same columns (use the union of all environment names)
- When bundle tag is unknown: show `-`
- `kardinal get pipelines --namespace all-ns` (if supported) must also use the new format

## Files to Modify

- `cmd/kardinal/cmd/get.go` — FormatPipelineTable function
- `cmd/kardinal/cmd/get_test.go` — update/add tests for new format

## Tasks

- [x] T001 Read current FormatPipelineTable implementation
- [x] T002 Write failing test for new column format
- [x] T003 Implement new per-environment column format
- [x] T004 Verify go test -race passes
