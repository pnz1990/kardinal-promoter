# Spec 902: kubectl printer columns for Bundle and PromotionStep CRDs

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 3: Observability — kubectl get bundle prints only phase, not current step`
- **Implements**: `kubectl get bundle` shows pipeline name; `kubectl get promotionstep` shows pipeline, environment, bundle context (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `kubectl get bundle -n <ns>` output MUST include a `PIPELINE` column showing `spec.pipeline`.

**O2**: `kubectl get promotionstep -n <ns>` output MUST include a `PIPELINE` column showing `spec.pipelineName`.

**O3**: `kubectl get promotionstep -n <ns>` output MUST include an `ENV` column showing `spec.environment`.

**O4**: `kubectl get promotionstep -n <ns>` output MUST include a `BUNDLE` column showing `spec.bundleName`.

**O5**: The `+kubebuilder:printcolumn` annotations in `api/v1alpha1/bundle_types.go` MUST match the `additionalPrinterColumns` entries in `config/crd/bases/kardinal.io_bundles.yaml`.

**O6**: The `+kubebuilder:printcolumn` annotations in `api/v1alpha1/promotionstep_types.go` MUST match the `additionalPrinterColumns` entries in `config/crd/bases/kardinal.io_promotionsteps.yaml`.

**O7**: Column ordering for Bundle MUST be: `Type`, `Pipeline`, `Phase`, `Age` (pipeline added after type, before phase).

**O8**: Column ordering for PromotionStep MUST be: `Pipeline`, `Env`, `Bundle`, `State`, `Age`.

**O9**: No existing tests may be broken by this change (it is purely additive — annotations and CRD YAML only).

---

## Zone 2 — Implementer's judgment

- Column name abbreviation (e.g. "ENV" vs "Environment") for readability in narrow terminals.
- Whether to include the `priority=1` flag on lower-priority columns.

---

## Zone 3 — Scoped out

- Adding an "Active Step" column to Bundle (requires status field not yet tracked).
- Adding a "Message" column to PromotionStep (too verbose for table output).
- Regenerating CRDs via `make manifests` (controller-gen may not be available; manual YAML update is acceptable).
