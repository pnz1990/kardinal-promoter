# Item 002: kubebuilder CRD Scaffold + controller-gen

> **Queue**: queue-001 (Stage 0)
> **Branch**: `002-kubebuilder-scaffold`
> **Depends on**: 001-go-module-scaffold
> **Dependency mode**: `branch` — only requires 001's branch to exist on remote (go.mod must be present); does not require 001 PR to be merged
> **Assignable**: after 001 branch pushed to remote
> **Contributes to**: All journeys (type system foundation)

---

## Goal

Add kubebuilder scaffolding for the four CRD types: `Pipeline`, `Bundle`, `PolicyGate`,
`PromotionStep`. The project uses `pkg/` layout (per AGENTS.md), **not** `internal/`.
Use controller-gen directly rather than `kubebuilder init` (which would overwrite the
`pkg/` layout with `internal/`).

The actual type fields (spec, status, validation) are implemented in Stage 1 (queue-002).
This item only creates the scaffolded stub types and wires controller-gen.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 0 (kubebuilder scaffold deliverable)

---

## Deliverables

1. Stub Go types in `pkg/` layout:
   - `pkg/graph/types.go` — stub `Graph`, `GraphSpec`, `GraphNode`, `GraphStatus` types (no kubebuilder markers; Graph is a kro CRD, not ours)
   - `pkg/reconciler/promotionstep/types.go` — stub `PromotionStep` struct with `+kubebuilder:object:root=true +kubebuilder:subresource:status` markers
   - `pkg/reconciler/policygate/types.go` — stub `PolicyGate` struct with markers
   - Create `api/v1alpha1/` directory for user-facing CRD types:
     - `api/v1alpha1/pipeline_types.go` — stub `Pipeline`, `PipelineSpec`, `PipelineStatus`
     - `api/v1alpha1/bundle_types.go` — stub `Bundle`, `BundleSpec`, `BundleStatus`
     - `api/v1alpha1/policygate_types.go` — stub `PolicyGate`, `PolicyGateSpec`, `PolicyGateStatus`
     - `api/v1alpha1/promotionstep_types.go` — stub `PromotionStep`, `PromotionStepSpec`, `PromotionStepStatus`
     - `api/v1alpha1/groupversion_info.go` — scheme registration
2. `Makefile` `generate` target: `$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."`
3. `Makefile` `manifests` target: `$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases`
4. `hack/boilerplate.go.txt` with Apache 2.0 header template
5. `CONTROLLER_GEN_VERSION` pinned in Makefile
6. All generated files have Apache 2.0 header (via boilerplate)
7. `config/samples/` directory with one stub manifest per user-facing CRD (Pipeline, Bundle, PolicyGate)

---

## Acceptance Criteria

- [ ] `make generate` runs without error and regenerates `zz_generated.deepcopy.go`
- [ ] `make manifests` regenerates CRD YAML in `config/crd/bases/` without diff (idempotent)
- [ ] `go build ./...` succeeds after running `make generate`
- [ ] `go test ./... -race` passes
- [ ] `config/crd/bases/` contains YAML for all 4 CRD kinds
- [ ] `config/samples/` contains stub manifests for Pipeline, Bundle, PolicyGate
- [ ] No banned filenames in new files
- [ ] All new `.go` files have Apache 2.0 header

---

## Self-Validation Commands

```bash
make generate
make manifests
git diff config/crd/bases/   # must be empty after re-running manifests
make build
make test
```

---

## Journey Contribution

Prerequisite for Stage 1 (CRD type definitions). No CRD-based journey step passes
without this scaffolding in place.
