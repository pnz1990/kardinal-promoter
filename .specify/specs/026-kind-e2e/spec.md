# Spec: Kind Cluster E2E GitHub Actions Workflow

> Feature branch: `026-kind-e2e`
> Stage: Cross-cut E2E verification
> Depends on: 023

## User Scenarios

### Scenario 1: J1 Quickstart passes on kind cluster
Given a kind cluster with krocodile and kardinal-promoter installed
When the E2E workflow runs
Then examples/quickstart/pipeline.yaml applies successfully
And a Bundle promotion completes through test → uat stages
And the prod PR is opened (or dry-run equivalent fires)

### Scenario 2: CI workflow runs on every main push
Given a push to main branch
When the GitHub Actions e2e workflow triggers
Then the kind cluster is created, krocodile installed, kardinal installed
And the journey tests run and pass
And the cluster is cleaned up after the run

### Scenario 3: Workflow uses dry-run for GitHub API calls
Given no real GitHub PAT is available in CI
When the E2E workflow runs
Then GitHub PR creation uses a dry-run/mock mode
And the test still verifies the promotion state machine reaches WaitingForMerge

## Requirements

- FR-001: `.github/workflows/e2e.yml` MUST create a kind cluster using `hack/install-krocodile.sh`
- FR-002: The workflow MUST install kardinal-promoter via `make install`
- FR-003: The workflow MUST run `make test-e2e` after cluster setup
- FR-004: The workflow MUST clean up the kind cluster after the run (even on failure)
- FR-005: The workflow MUST use `dry-run: true` mode for GitHub SCM calls to avoid requiring a real PAT
- FR-006: `TestJourney1Quickstart` MUST pass (removing t.Skip) when dry-run mode is active
- FR-007: The workflow MUST run on push to main and on workflow_dispatch

## Go Package Structure

```
test/e2e/
  e2e_test.go           # TestInfrastructure (already exists)
  journeys_test.go      # Remove t.Skip from J1, J3 when dry-run active
  dry_run_server_test.go # HTTP test server that mocks GitHub API for PR creation
.github/workflows/
  e2e.yml               # Updated workflow with krocodile install + dry-run flag
```

## Success Criteria

- SC-001: `make test-e2e` passes on a real kind cluster in CI
- SC-002: `TestJourney1Quickstart` passes without t.Skip in dry-run mode
- SC-003: E2E workflow runs end-to-end in < 15 minutes
- SC-004: Kind cluster is always cleaned up (deferred in workflow)
