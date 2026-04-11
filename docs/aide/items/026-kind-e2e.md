# Item 026: Kind Cluster E2E GitHub Actions Workflow

> **Stage**: Cross-cut (E2E verification)
> **Queue**: queue-012
> **Priority**: high
> **Size**: l
> **Depends on**: 023 (fake-client E2E tests)
> **dependency_mode**: merged

## Context

Item 023 added fake-client E2E tests (J1/J3/J4/J5) that pass in CI without a live cluster.
These are valuable unit-level tests but do NOT satisfy the definition-of-done requirement for
marking journeys ✅. A real kind cluster run is required.

This item adds a GitHub Actions workflow that:
1. Spins up a kind cluster
2. Builds and installs kardinal-promoter (Helm chart)
3. Applies examples/quickstart/pipeline.yaml
4. Runs a mock promotion using `kubectl` and fake GitHub responses
5. Verifies J1/J3 pass end-to-end

Note: Full E2E requires a real GitHub token for PR creation. For CI, we mock the GitHub API
using an HTTP test server or use the `dry-run` mode that skips actual GitHub calls. The CI
workflow uses the dry-run path; the passing criteria is the promotion state machine reaching
the expected state.

## Acceptance Criteria

- `.github/workflows/e2e.yml` workflow:
  - Sets up kind cluster (using `helm/kind-action` or `kubernetes-sigs/setup-kind`)
  - Builds and loads the controller image
  - Installs kro (Graph controller) from krocodile pinned commit `1b0ce353`
  - Installs kardinal-promoter via Helm
  - Applies `examples/quickstart/pipeline.yaml`
  - Creates a Bundle via `kardinal create bundle nginx-demo --image ghcr.io/nginx/nginx:1.29.0 --dry-run` (no real GitHub calls)
  - Verifies Bundle reaches `Available` and then `Promoting` state
  - Verifies PolicyGate no-weekend-deploys evaluates correctly
  - Verifies `kardinal explain nginx-demo --env prod` shows gate trace
- Workflow runs on: push to main, PRs labeled `kardinal`, manual dispatch
- Workflow timeout: 15 minutes
- `make test-e2e-journey-1` target added to Makefile that runs the kind workflow locally

## Files to Create/Modify

- `.github/workflows/e2e.yml` — new kind cluster E2E workflow
- `Makefile` — add `test-e2e-journey-1` and `test-e2e-journey-3` targets
- `hack/e2e-setup.sh` — kind cluster setup script (reusable for local dev)
- `hack/e2e-teardown.sh` — cleanup script
- `examples/quickstart/policy-gates.yaml` — ensure exists with no-weekend-deploys gate
- `test/e2e/kind_test.go` — Go test that runs against a real cluster (skipped if KUBECONFIG not set)

## Tasks

- [ ] T001 Create `.github/workflows/e2e.yml` with kind setup, image build, helm install
- [ ] T002 Add kro/krocodile installation step (pinned commit `1b0ce353`)
- [ ] T003 Create `hack/e2e-setup.sh` and `hack/e2e-teardown.sh`
- [ ] T004 Add `test/e2e/kind_test.go` with TestJourney1_KindCluster (skips without KUBECONFIG)
- [ ] T005 Add `make test-e2e-journey-1` target
- [ ] T006 Verify `examples/quickstart/policy-gates.yaml` exists and has no-weekend-deploys
- [ ] T007 Verify workflow passes in CI (push to branch, check Actions tab)
