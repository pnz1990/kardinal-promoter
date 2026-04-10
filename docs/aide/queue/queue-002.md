# Queue 002 — Stage 1: CRD Types and Validation

> **Generated**: 2026-04-10
> **Stage**: 1 — CRD Types and Validation
> **Roadmap ref**: docs/aide/roadmap.md Stage 1
> **Batch size**: 1 item
> **Status**: active

---

## Purpose

Define the complete Go types and OpenAPI validation schemas for all user-facing CRDs.
The scaffold (Stage 0) created stub types with partial fields. Stage 1 completes them
with all roadmap-specified fields, kubebuilder validation markers, generated CRD YAML,
and sample manifests.

No running controller logic is implemented in this queue. Only the schema artifacts
that every downstream stage depends on for type safety.

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 005 | 005-crd-types-and-validation | Complete CRD types, validation markers, generated YAML, and roundtrip tests | 001,002,003,004 | immediately |

---

## Assignment Wave 1

- **005-crd-types-and-validation** → ENGINEER-1 (no blockers, assign immediately)

---

## Acceptance Gate

Item 005 `done` in `.maqa/state.json` before advancing to Stage 2 queue.

- `kubectl apply -f config/crd/bases/` against a fresh kind cluster installs all CRDs without error
- `kubectl apply -f config/samples/` creates valid objects that pass server-side validation
- `go vet ./...` and `golangci-lint` pass with no new warnings
- Roundtrip JSON marshaling test passes for each CRD type
