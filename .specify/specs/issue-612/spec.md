# Spec: forEach Multi-Region Fan-Out (issue-612)

> Status: Active
> Author: sess-7b9a4752
> Created: 2026-04-22

## Design reference

- **Design doc**: `docs/design/02-pipeline-to-graph-translator.md`
- **Section**: `§ Step 5: Build Graph nodes`
- **Implements**: Multi-region PromotionStep fan-out using krocodile `forEach` primitive (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1.** `EnvironmentSpec` in `api/v1alpha1/pipeline_types.go` MUST include a `Regions []string`
field with JSON tag `regions,omitempty`. A pipeline with `regions: [us-east-1, eu-west-1]`
on an environment is valid YAML.

**O2.** `PromotionStepSpec` in `api/v1alpha1/promotionstep_types.go` MUST include a `Region string`
field with JSON tag `region,omitempty`. This field is set by the krocodile forEach `${item}`
template expression — it is NOT set manually.

**O3.** When `len(env.Regions) > 1`, `buildNodes` in `pkg/graph/builder.go` MUST emit a Graph
node with `ForEach` set to a CEL expression referencing the regions collection. The node ID
for the forEach node MUST be the `celSafeSlug(envName)` of the environment (same as today for
single-region environments — preserving downstream `propagateWhen` references unchanged).

**O4.** When `len(env.Regions) <= 1` (including zero — single-region or not specified),
`buildNodes` MUST behave identically to today. All existing tests MUST pass without modification.

**O5.** The forEach PromotionStep template MUST set `spec.region` to `${item}` so the reconciler
can read which region it's operating in.

**O6.** The `propagateWhen` on the forEach node MUST use a list-aggregate pattern:
`${celSafeSlug(envName)_LIST.all(s, s.status.state == "Verified")}` — meaning ALL regional
PromotionSteps must be Verified before downstream can proceed. This is the correct fan-out
semantics.

**O7.** `zz_generated.deepcopy.go` MUST be regenerated (or updated manually) to include
`DeepCopyInto` handling for the new `Regions` slice field and `Region` string field.

**O8.** A test `TestBuilder_MultiRegionFanOut` MUST exist in `pkg/graph/builder_test.go`
verifying: when `env.Regions = ["us-east-1", "eu-west-1"]`, the Graph node has `ForEach` set
and `spec.region = "${item}"` in the template.

---

## Zone 2 — Implementer's judgment

- The exact CEL expression for `ForEach` field: use a Go slice literal in the template
  spec that references regions statically, OR use the forEach primitive that iterates over
  the PromotionStep spec.regions list. Evaluate which krocodile forEach syntax works with
  our pinned commit (d6cbc54).
- Whether `propageWhen` aggregate uses `all()` or another approach: use what krocodile supports.
- Whether to include `regions` in the PromotionStep inputs map vs. as a dedicated field.

---

## Zone 3 — Scoped out

- Dynamic environment discovery (use case #3 from issue: WatchKind + forEach) — not in scope.
- Step sequence fan-out (use case #4) — not in scope.
- PolicyGate fan-out (use case #2) — not in scope for this PR; follow-up issue.
- Changes to the PromotionStep reconciler behavior based on `spec.region` — the field is added
  but the reconciler uses it only for logging/labels in this PR. Full region-aware behavior is follow-up.
- Changes to `kardinal explain` or CLI output for multi-region pipelines — follow-up.
