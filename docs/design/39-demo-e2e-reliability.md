# 39: Demo E2E Reliability — No Flaky Tests, No Ignored CI

> Status: Active | Created: 2026-04-20
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
- ✅ `ci.yml` build job: added "Validate demo and example manifests against CRD schema" step — fails if any manifest references an unknown health sub-field

---

## Future (🔲)
- 🔲 **Graph builder `dependsOn` validation error is silent in CLI output** — `pkg/graph/builder.go:125` returns a `fmt.Errorf("build: environment %q dependsOn unknown environment %q", ...)` error when a pipeline references an unknown `dependsOn` target. This error propagates to the translator, which writes it to `Graph.status.conditions`. The CLI (`kardinal get pipelines`) does not surface this condition in its default tabular output — it shows `Phase: Error` at best, requiring `kubectl describe graph` to see the root cause. Add a `CONDITIONS` column or inline error message to `kardinal get pipelines` output when the graph build phase is `Error`. ⚠️ Inferred from code scan: `pkg/graph/builder.go:125` — user-facing error is buried in CRD status.
- 🔲 **`pkg/cel/conversion/conversion.go` `GoNativeType` returns `nil, nil` for a nil CEL value — ambiguous for callers** — `pkg/cel/conversion/conversion.go:32` returns `nil, nil` (no value, no error) when `v == nil`. Callers cannot distinguish "CEL expression evaluated to nil (a valid falsy result)" from "the CEL evaluator returned a nil ref.Val due to a missing variable or type error." In the PolicyGate reconciler, this means a nil CEL value can silently pass a gate (no error is returned, result is treated as non-blocking). Add an explicit `ErrNilCELValue` sentinel error returned when `v == nil`, so callers can distinguish evaluation-produced nil from evaluator failure. ⚠️ Inferred from code scan: `pkg/cel/conversion/conversion.go:32` — nil-is-success is a PolicyGate correctness risk.

- ✅ 39.1 — PDCA scenario for schema drift: add a PDCA scenario that creates a Pipeline manifest with an unknown field and asserts that `ci.yml` fails with the expected error. This makes the CI validation step itself testable. (PR #931)
- ✅ 39.2 — Update `README.md` examples section: the README may also reference the old `health.argoRollouts{}` syntax. Audit all docs for stale field references. (PR #898)
- ✅ 39.3 — Add kubeconform to `Makefile` as a `make validate-manifests` target so contributors can run it locally before pushing. (PR #1001, 2026-04-21)

---

## Zone 1 — Obligations

**O1 — Demo Validate must be green on every PR that touches its trigger paths.**
There are no acceptable "known flaky" failures. If Demo Validate is red, the PR does
not merge. Period. The agent must treat Demo Validate failures as real failures and fix
them, not bypass them with `--admin`.

**O2 — The CI manifest validation step runs on every push.**
It is not path-filtered. Every push rebuilds the validation to catch drift introduced by
API changes that don't touch `demo/`.

**O3 — The `health` schema is the source of truth.**
`api/v1alpha1/pipeline_types.go` `HealthSpec` struct defines the allowed fields. Any
manifest field not present in that struct is an error, not a documentation omission.

---

## Zone 2 — Implementer's judgment

- The CI manifest validation uses Python+yaml parse (no cluster needed) rather than
  `kubectl --dry-run` (requires a running cluster). The Python check is sufficient for
  detecting unknown fields; it does not validate field values, only field names.
- kubeconform would be the ideal long-term solution (full JSON Schema validation). It is
  listed as a Future item (39.3) but the Python fallback is sufficient for now.

---

## Zone 3 — Scoped out

- Validating non-Pipeline CRDs in demo manifests (Bundle, PRStatus) — lower risk, add if needed
- Auto-fixing manifests when the API changes (too risky for automation)
