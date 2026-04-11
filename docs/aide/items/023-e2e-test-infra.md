# Item 023: E2E Test Infrastructure — Kind Cluster CI (Stage cross-cut)

> **Queue**: queue-011
> **Branch**: `023-e2e-test-infra`
> **Depends on**: 013 (PromotionStep reconciler, merged), 014 (health adapters, merged)
> **Dependency mode**: merged
> **Contributes to**: J1 (Quickstart), J3 (Policy governance), J4 (Rollback), J5 (CLI)
> **Priority**: HIGH — unlocks journey ✅ verification for v0.3.0 release

---

## Goal

Add an end-to-end test suite that runs on a kind cluster in CI, verifying the core
promotion loop (J1), policy gate evaluation (J3), rollback (J4), and CLI commands (J5).
This is the final gate before cutting a v0.3.0 release.

---

## Deliverables

### 1. Kind cluster E2E test job in `.github/workflows/ci.yml`

Add a new CI job `e2e-kind` that:
- Spins up a kind cluster with `kind create cluster`
- Installs the krocodile Graph controller (pinned commit `1b0ce353`)
- Applies CRDs via `kubectl apply -f config/crd/bases/`
- Builds and loads the controller image into kind
- Applies a test Pipeline and Bundle YAML from `test/e2e-kind/fixtures/`
- Asserts Bundle advances to `Verified` within 120 seconds
- Asserts `kardinal get pipelines` shows correct output
- Asserts `kardinal policy simulate` returns expected BLOCKED result on weekend mock

### 2. E2E test fixtures in `test/e2e-kind/fixtures/`

- `pipeline.yaml` — 3-env (test, uat, prod) pipeline with `approvalMode: auto` for test/uat and `pr-review` for prod
  (Use `approvalMode: auto` for all envs in CI to avoid real GitHub PR flow)
- `bundle.yaml` — image Bundle with a real image ref (ghcr.io/nginx/nginx:1.29.0)
- `policy-gates.yaml` — `no-weekend-deploys` gate for prod

### 3. E2E test script `test/e2e-kind/run.sh`

Shell script that:
1. Applies fixtures
2. Creates a Bundle via `kardinal create bundle`
3. Polls `kubectl get bundle` until phase=Verified (timeout 120s)
4. Runs `kardinal get pipelines` and validates output format
5. Runs `kardinal policy simulate --pipeline ... --env prod --time "Saturday 3pm"` and checks for "BLOCKED"
6. Exits 0 on success, 1 on failure with diagnostic output

### 4. Makefile targets

- `make test-e2e-kind` — runs the full E2E test locally against current kubeconfig
- `make test-e2e-journey-1` — alias for e2e test focusing on J1

### 5. Update definition-of-done.md

After the CI job passes, update J1, J3, J4, J5 journey statuses to ✅.

---

## Acceptance Criteria

- [ ] CI job `e2e-kind` passes on a clean push to main
- [ ] Bundle advances through test → uat → prod (auto-approve) within 120s
- [ ] `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED
- [ ] `kardinal get pipelines` output matches expected table format
- [ ] `make test-e2e-kind` runs locally against a kind cluster
- [ ] `go build ./...` passes
- [ ] `go vet ./...` passes
- [ ] No banned filenames
