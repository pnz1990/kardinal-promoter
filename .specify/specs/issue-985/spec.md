# Spec: issue-985 — PromotionTemplate CRD

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — No reusable PromotionTemplate concept`
- **Implements**: `PromotionTemplate` CRD (named step sequence, referenced by Pipeline environments) (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** A new CRD `PromotionTemplate` (kind: PromotionTemplate, group: kardinal.io/v1alpha1) exists with:
  - `spec.steps` — `[]StepSpec` (same structure as `EnvironmentSpec.Steps`)
  - `spec.description` — optional string
  Violation: CRD is absent from `config/crd/` and `api/v1alpha1/`.

**O2** `EnvironmentSpec` gains an optional field `promotionTemplate` (type: `*PromotionTemplateRef`):
  ```go
  type PromotionTemplateRef struct {
      Name      string `json:"name"`
      Namespace string `json:"namespace,omitempty"`
  }
  ```
  Violation: field is absent from `api/v1alpha1/pipeline_types.go`.

**O3** The graph builder resolves `promotionTemplate` reference before generating nodes:
  - When `env.PromotionTemplate != nil` AND `env.Steps` is empty: inline the referenced template's `spec.steps` into the EnvironmentSpec.
  - When both `env.PromotionTemplate != nil` AND `env.Steps` is non-empty: `env.Steps` wins (local override).
  - When `env.PromotionTemplate == nil` AND `env.Steps` is empty: existing default behavior.
  Violation: a Pipeline env with `promotionTemplate: foo` and empty steps uses default steps (not template steps).

**O4** Template resolution happens in the `Translator`, not the `Builder`. The `Builder` receives an already-resolved `Pipeline` (steps inlined). This keeps the `Builder` pure (no k8s client).
  Violation: `pkg/graph/builder.go` imports `client.Reader` or fetches PromotionTemplate.

**O5** When the referenced `PromotionTemplate` does not exist, `Translator.Translate` returns an error (causes Bundle phase=Failed with reason "TemplateNotFound"). 
  Violation: translation succeeds silently with empty steps.

**O6** `pkg/graph/builder_test.go` has at least one table test case for PromotionTemplate inlining (two envs sharing the same template, and local-override takes precedence).
  Violation: test cases absent.

**O7** `docs/concepts.md` contains a `## PromotionTemplate` section documenting the new CRD.
  Violation: section absent.

**O8** `deepcopy` is regenerated (`zz_generated.deepcopy.go` contains `DeepCopyObject` for `PromotionTemplate`).
  Violation: missing generated code.

---

## Zone 2 — Implementer's judgment

- CRD manifest location: `config/crd/bases/kardinal.io_promotiontemplates.yaml`
- Whether to add a Helm template for PromotionTemplate RBAC (recommend: yes, same as Pipeline)
- Test naming conventions
- Whether to add `kardinal get templates` CLI command (nice-to-have, not required for this item)

---

## Zone 3 — Scoped out

- CLI command `kardinal get templates` (separate issue)
- UI display of PromotionTemplates (separate issue)
- Cross-namespace template references requiring RBAC (namespace defaults to Pipeline's namespace)
- `kardinal init` generating a PromotionTemplate example (separate enhancement)
