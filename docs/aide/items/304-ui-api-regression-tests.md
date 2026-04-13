# Item 304: UI API Regression Tests for issue #410

> Issue: #410 (partial)
> Queue: queue-015
> Milestone: v0.6.0-proof
> Size: s
> Priority: high
> Area: area/test
> Kind: kind/enhancement
> Depends on: 019-embedded-react-ui (merged)

## Context

Issue #410 (proof(UI)) requires evidence that UI panels show correct data.
The "176 PolicyGates blocking promotion" bug was fixed but not regression-tested.
The PAUSED badge in the pipeline list also needs a test.

## Acceptance Criteria

### AC1: /api/v1/ui/gates returns exactly N gates for N CRs (no duplicates)
### AC2: Paused pipeline is reflected in the pipeline list response

## Tasks

- [x] Add TestUIAPI_ListGates_NoDuplicates (3 distinct gate CRs → 3 response items)
- [x] Add TestUIAPI_ListPipelines_PausedBadge (paused pipeline → Paused=true in response)
- [x] Run go test ./cmd/kardinal-controller/... -race — all pass
