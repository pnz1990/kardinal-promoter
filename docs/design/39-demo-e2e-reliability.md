# 39: Demo E2E Reliability — No Flaky Tests, No Ignored CI

> Status: Active | Created: 2026-04-20 | Validation section corrected 2026-09-29
> Applies to: kardinal-promoter

---

## What this does

The Demo Validate workflow (`demo-validate.yml`) was failing on every PR with:

```
strict decoding error: unknown field "spec.environments[0].health.argoRollouts"
```

This looked like a flaky test (it only triggers on PRs touching `demo/`, `pkg/`, `api/`)
but was a real, deterministic bug: the demo manifests referenced `health.argoRollouts{}`
and `health.flagger{}` sub-fields that were never part of the CRD schema. The controller
derives these automatically from pipeline name and environment namespace — they are internal
implementation details, not user-facing API fields.

Every run of Demo Validate failed at the same step, for the same reason. Because it
appeared intermittent (only triggered by certain PR paths), it was being bypassed with
`--admin` merges. This trained the team to treat a red Demo Validate as normal. That is
the most dangerous state: a test that always fails is indistinguishable from a test that
occasionally fails for a real reason.

**A flaky or routinely-failing test is not a test. It is noise that teaches you to ignore CI.**

The fix has two parts:
1. Remove the invalid sub-fields from all affected manifests (immediate)
2. Add a CI step that validates all `demo/` and `examples/` Pipeline manifests against
   the generated CRD schema on every build (prevention)

---

## Present (✅)

- ✅ Fixed `demo/manifests/rollouts/pipeline.yaml` — removed `health.argoRollouts{}` block
- ✅ Fixed `demo/manifests/flagger/pipeline.yaml` — removed `health.flagger{}` block
- ✅ Fixed `examples/argo-rollouts-demo/pipeline.yaml` — removed `health.argoRollouts{}` and `health.argocd{}`
- ✅ Fixed `examples/multi-cluster-fleet/pipeline.yaml` — removed `health.argoRollouts{}` (×2)
- ✅ Fixed `examples/flagger-demo/pipeline.yaml` — removed `health.flagger{}`
- ✅ `ci.yml` build job: added "Validate demo and example manifests against CRD schema" step. The first version only checked `health` sub-keys against a fixed list. Since #1247 it runs `go test ./test/examples/...` (`TestExampleManifestsMatchCRDSchemas`), which checks every kardinal.io object in `examples/`, `demo/` and `test/pdca/` against the generated CRD schemas

---

## Future (🔲)
- ✅ `cmd/kardinal/cmd`: `FormatBundleErrors` surfaces Failed-phase bundles with their `TranslationError` / `CircularDependency` condition message after the `get pipelines` table — operators no longer need `kubectl describe graph` to find the root cause. `getPipelinesOnce` fetches Bundles (non-fatal on error) and calls `FormatBundleErrors`. (PR #1048, 2026-04-22)
- ✅ **`pkg/cel/conversion/conversion.go` `GoNativeType` returns `nil, nil` for a nil CEL value — ambiguous for callers** — Added `ErrNilCELValue` sentinel; `GoNativeType(nil)` now returns `(nil, ErrNilCELValue)` so callers can distinguish evaluator failure from CEL null (`types.NullType` still returns `(nil, nil)`). Tests cover both cases. (PR #1089, 2026-04-22)

- ✅ 39.1 — PDCA scenario for schema drift: add a PDCA scenario that creates a Pipeline manifest with an unknown field and asserts that `ci.yml` fails with the expected error. This makes the CI validation step itself testable. (PR #931)
- ✅ 39.2 — Update `README.md` examples section: the README may also reference the old `health.argoRollouts{}` syntax. Audit all docs for stale field references. (PR #898)
- ✅ 39.3 — `make validate-manifests` lets contributors run the check locally before pushing. (PR #1001, 2026-04-21) It runs the same Go test as CI; kubeconform is not used.
- ✅ 39.4 — Fix PDCA S1 flap: `readyz` probe now gates on informer cache sync — pod reports Ready only after the cache is populated and reconcilers can process events. `helm --wait` now guarantees reconciliation is active before tests run. S1 timeout increased from 5min (20×15s) to 7.5min (30×15s) as defense-in-depth (the current `pdca.yml` waits 5 minutes: `wait_state … 300`). Root cause: `healthz.Ping` was used for `/readyz`, allowing the pod to report Ready before `WaitForCacheSync` completed (~5min on resource-constrained CI runners). (PR #1132, 2026-04-23)

---

## Zone 1 — Obligations

**O1 — Demo Validate must be green on every PR that touches its trigger paths.**
There are no acceptable "known flaky" failures. If Demo Validate is red, the PR does
not merge. Period. The agent must treat Demo Validate failures as real failures and fix
them, not bypass them with `--admin`.

**O2 — The CI manifest validation step runs on every push.**
It is not path-filtered. Every push rebuilds the validation to catch drift introduced by
API changes that don't touch `demo/`.

**O3 — The generated CRD schema is the source of truth.**
The Go API types in `api/v1alpha1/` (for `health`, the `HealthConfig` struct in
`pipeline_types.go`) generate the schemas in `config/crd/bases/`. Any manifest field the
schema does not define is an error, not a documentation omission.

---

## Zone 2 — Implementer's judgment

- The CI manifest validation needs no cluster. `test/examples` decodes each manifest and
  validates it against the generated CRD schemas with the apiextensions schema validator
  (unknown fields, enums, patterns), instead of `kubectl --dry-run`, which needs a cluster.
  kubeconform is not needed.

---

## Zone 3 — Scoped out

- Validating non-Pipeline CRDs in demo manifests (Bundle, PRStatus) — lower risk, add if needed
- Auto-fixing manifests when the API changes (too risky for automation)
