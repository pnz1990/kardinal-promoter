# Item 002: kubebuilder CRD Scaffold + controller-gen

> **Queue**: queue-001 (Stage 0)
> **Branch**: `002-kubebuilder-scaffold`
> **Depends on**: 001-go-module-scaffold (must be `done` in state.json)
> **Assignable**: after item 001 merged to main
> **Contributes to**: All journeys (type system foundation)

---

## Goal

Run `kubebuilder init` and `kubebuilder create api` to generate the scaffolding for
three CRDs: `Pipeline`, `Bundle`, `PolicyGate`. Also scaffold the internal
`PromotionStep` controller-internal type. No Go type fields yet — only the
kubebuilder-generated stubs and `controller-gen` markers are added here.

The actual type fields (spec, status, validation) are implemented in Stage 1
(queue-002).

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 0 (kubebuilder scaffold deliverable)

---

## Deliverables

1. `kubebuilder init --domain kardinal.io --repo github.com/kardinal-promoter/kardinal-promoter`
   output committed (PROJECT file, config/default/, config/manager/, etc.)
2. `kubebuilder create api --group kardinal --version v1alpha1 --kind Pipeline` scaffold
3. `kubebuilder create api --group kardinal --version v1alpha1 --kind Bundle` scaffold
4. `kubebuilder create api --group kardinal --version v1alpha1 --kind PolicyGate` scaffold
5. `kubebuilder create api --group kardinal --version v1alpha1 --kind PromotionStep` scaffold
   (internal, not user-created; add `+kubebuilder:resource:scope=Namespaced` marker)
6. `Makefile` updated: `generate` target calls `controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."`,
   `manifests` target generates CRD YAML to `config/crd/bases/`
7. `hack/boilerplate.go.txt` with Apache 2.0 header template
8. `controller-gen` pinned in `Makefile` via `CONTROLLER_GEN_VERSION`
9. All generated files have Apache 2.0 header (via boilerplate)
10. `config/samples/` directory with one stub manifest per CRD

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
