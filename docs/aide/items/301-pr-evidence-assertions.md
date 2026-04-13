# Item 301: PR Body Field Assertions in TestJourney1Quickstart

> Issue: #412
> Queue: queue-015
> Milestone: v0.6.0-proof
> Size: m
> Priority: high
> Area: area/test
> Kind: kind/enhancement
> Depends on: 023-e2e-test-infra (merged)

## Context

The `docs/pr-evidence.md` documentation claims every promotion PR contains a structured body
with specific evidence fields. As of 2026-04-13, there is no automated test verifying this.
The existing `TestJourney1Quickstart` (in `test/journey/journey_test.go`) verifies the promotion
flow end-to-end with a fake K8s client but does NOT check what was in the PR body.

## Spec Reference

`docs/pr-evidence.md`, `pkg/scm/pr_template.go`

## Acceptance Criteria

### AC1: PR body contains all required fields (tested via fake client)
**Given** a TestJourney1Quickstart test with a fake SCMProvider that records PR bodies
**When** a Bundle is created and promotion runs through the fake client
**Then** the recorded PR body contains:
  - `bundle.spec.images[0].repository` and `bundle.spec.images[0].tag`
  - `bundle.spec.provenance.commitSHA` (or an indication of the commit)
  - A gate compliance table with at least one gate row
  - The pipeline and environment name

### AC2: PR body test is table-driven with all documented fields from pr-evidence.md
**Given** a table-driven test `TestPRBodyFields`
**When** a PR is opened by the step engine using the template
**Then** each field from `docs/pr-evidence.md` is present in the output

### AC3: No phantom completions — assertions are real field checks (not just "len > 0")

## Tasks

- [ ] Read `pkg/scm/pr_template.go` to understand what fields are included
- [ ] Read `test/journey/journey_test.go` to understand current test structure
- [ ] Add `TestPRBodyFields` table-driven test in `pkg/scm/` that verifies template output
- [ ] Check AC1 fields in the PR template output
- [ ] Run `go test ./pkg/scm/... -race` — all pass
- [ ] Run `go test ./test/journey/... -race` — all pass
- [ ] Post issue #412 with evidence

## Files to modify

- `pkg/scm/pr_template_test.go` (create or extend)
- `test/journey/journey_test.go` (extend existing test or add helper)
