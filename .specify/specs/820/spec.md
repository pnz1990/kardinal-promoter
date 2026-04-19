# Spec: feat(demo): full adapter coverage — Flux, Flagger, Argo Rollouts, GitHub examples + tests + docs

Issue: #820
Date: 2026-04-18
Author: sess-1f914935

## Design reference
- **Design doc**: `docs/design/05-health-adapters.md`
- **Section**: `§ Future`
- **Implements**: Full demo coverage for all 5 health adapters (resource ✅, argocd ✅, flux 🔲, argoRollouts partial, flagger 🔲)

---

## Zone 1 — Obligations (falsifiable)

O1. `examples/flux-demo/` exists with a valid `pipeline.yaml` using `health.type: flux`,
    plus a `README.md` explaining setup and a `flux-kustomization.yaml` fixture.
    Violation: directory missing or pipeline.yaml does not specify `health.type: flux`.

O2. `examples/flagger-demo/` exists with a valid `pipeline.yaml` using `health.type: flagger`,
    plus a `README.md` and a `canary.yaml` fixture.
    Violation: directory missing or wrong health type.

O3. `examples/argo-rollouts-demo/` exists as a standalone single-cluster example (separate
    from multi-cluster-fleet) with `health.type: argoRollouts`, a Rollout fixture, and README.
    Violation: directory missing or no Rollout fixture.

O4. `examples/github-demo/` exists demonstrating all GitHub SCM features: PR review gate,
    pr-evidence body, emergency override comment, rollback PR. README documents each.
    Violation: missing or incomplete (does not mention override or rollback).

O5. `pkg/health/health_test.go` contains unit tests for ALL five adapters.
    Specifically: ArgoRollouts healthy, ArgoRollouts degraded, ArgoRollouts not-found,
    Flagger succeeded, Flagger failed, Flagger not-found.
    Violation: any of these 6 test cases missing.

O6. `scripts/demo-validate.sh` exists, is executable, runs `go test ./pkg/health/...`
    with `-race -count=1`, and exits non-zero on failure.
    Violation: script missing, not executable, or does not run the health tests.

O7. `docs/demo-validation.md` exists and documents:
    - Each of the 5 adapters: what it checks, what "healthy" means, example Pipeline YAML snippet
    - How to run `scripts/demo-validate.sh`
    - Known limitations per adapter
    Violation: any adapter undocumented, or document does not include example YAML.

O8. `docs/design/05-health-adapters.md` is updated: the 6 items above moved from 🔲 to ✅.
    Violation: design doc not updated.

O9. `go build ./...` and `go test ./... -race -count=1 -timeout 120s` pass with no new failures.
    Violation: any new test failure or build error.

---

## Zone 2 — Implementer's judgment

- Flux demo can use `kardinal-demo` repo as the GitOps target (same as quickstart)
- Flagger demo uses `namespace: flagger-system` for the canary (standard Flagger install)
- Argo Rollouts demo uses `namespace: default` — single cluster, no sharding
- GitHub demo uses all the same `pnz1990/kardinal-demo` repo
- Unit tests use fake/dynamic clients (no real cluster needed)

---

## Zone 3 — Scoped out

- Live cluster validation (kind) — that is for the product validation cycle
- GitLab or Forgejo SCM demos
- Helm update strategy examples (separate from health)
- Prometheus/MetricCheck demos
