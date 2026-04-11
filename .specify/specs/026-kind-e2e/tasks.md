# Tasks: Kind Cluster E2E GitHub Actions Workflow

**Input**: `.specify/specs/026-kind-e2e/spec.md`
**Feature branch**: `026-kind-e2e`
**Test command**: `go test ./test/e2e/... -race`

---

## Phase 1: Setup

- [ ] T001 Add `DRY_RUN` environment variable support to SCM provider: when set, skip real GitHub API calls and return mock PR URLs — file: `pkg/scm/github.go`
- [ ] T002 [P] Add `dry_run_server_test.go` with an HTTP test server that mocks the GitHub API (POST /repos/{owner}/{repo}/pulls → returns fake PR) — file: `test/e2e/dry_run_server_test.go`

## Phase 2: Tests First

- [ ] T003 Write `TestJourney1Quickstart` test body (replacing t.Skip): apply quickstart pipeline, create bundle, verify promotion reaches WaitingForMerge state using fake-client + dry-run SCM — file: `test/e2e/journeys_test.go`
- [ ] T004 [P] Write `TestJourney3PolicyGovernance` test body (replacing t.Skip): verify weekend gate blocks prod promotion — file: `test/e2e/journeys_test.go`
- [ ] T005 [P] Write `TestInfrastructureDryRun` that verifies the dry-run HTTP server correctly intercepts GitHub PR creation calls — file: `test/e2e/dry_run_server_test.go`

## Phase 3: Implementation

- [ ] T006 Implement dry-run mode in `pkg/scm/github.go`: check `DRY_RUN` env var, return mock PR URL without calling GitHub API — file: `pkg/scm/github.go`
- [ ] T007 Update `.github/workflows/e2e.yml`: add `DRY_RUN=true` env var, ensure krocodile install step runs via `hack/install-krocodile.sh`, add cluster cleanup step — file: `.github/workflows/e2e.yml`
- [ ] T008 [P] Remove `t.Skip` from `TestJourney1Quickstart` and `TestJourney3PolicyGovernance` in journeys_test.go — file: `test/e2e/journeys_test.go`
- [ ] T009 Update `Makefile` `test-e2e` target to set `DRY_RUN=true` when `KIND_CLUSTER` env var is set — file: `Makefile`

## Phase 4: Validation

- [ ] T010 Run `go test ./test/e2e/... -race -run TestInfrastructure` locally against a kind cluster to verify krocodile CRDs are present — file: `test/e2e/e2e_test.go`
- [ ] T011 Run `go test ./test/e2e/... -race -run TestJourney1` with `DRY_RUN=true` to verify fake-client path passes — file: `test/e2e/journeys_test.go`
- [ ] T012 Run /speckit.verify-tasks.run to confirm no phantom completions
