# Spec: CI check CRD printer columns vs Go type annotations (Issue #904)

## Design reference
- **Design doc**: N/A — infrastructure change with no user-visible behavior change

---

## Zone 1 — Obligations (falsifiable)

**O1** — A Go test in `api/v1alpha1/` MUST parse `+kubebuilder:printcolumn` comments
from all `*_types.go` files and compare them against the `additionalPrinterColumns`
entries in the corresponding `config/crd/bases/*.yaml` files.

**O2** — The test MUST fail if a printer column present in Go annotations is absent
from the CRD YAML.

**O3** — The test MUST fail if a printer column in the CRD YAML has a different
`name`, `type`, or `jsonPath` than the Go annotation.

**O4** — The test MUST run under `go test ./api/...` with no external tools required.

---

## Zone 2 — Implementer's judgment

- Parse annotations from Go source using `go/scanner` or regex (regex simpler for single-line markers).
- Parse CRD YAML using `sigs.k8s.io/yaml` (already a dependency).
- Map from kind name → CRD file using the `kardinal.io_<plural>.yaml` naming convention.
- Priority annotation field (optional) — may differ between Go and YAML without causing test failure.

---

## Zone 3 — Scoped out

- Running `controller-gen` in the test (requires build toolchain; avoid).
- Checking CRD schema fields (only printer columns).
- Cross-checking YAML spec against validation markers.
