# Spec: issue-971 — make validate-manifests target

## Design reference
- **Design doc**: `docs/design/39-demo-e2e-reliability.md`
- **Section**: `§ Future`
- **Implements**: 39.3 — Add kubeconform to `Makefile` as a `make validate-manifests` target (🔲 → ✅)

---

## Zone 1 — Obligations

**O1** — A `make validate-manifests` target exists in `Makefile` that validates all Pipeline
manifests in `demo/` and `examples/` against the CRD schema.

**O2** — The target mirrors exactly what the CI step in `ci.yml` does: install kubeconform
(or use a Python fallback), extract the CRD schema, validate each Pipeline manifest, and
exit non-zero on any unknown field.

**O3** — The target is listed in `.PHONY` and documented with a `##` comment so it appears
in `make help`.

**O4** — Running `make validate-manifests` on a clean checkout with valid manifests exits 0.

**O5** — The design doc `docs/design/39-demo-e2e-reliability.md` is updated: the 39.3 item
moves from 🔲 to ✅ with a PR reference.

---

## Zone 2 — Implementer's judgment

- Use the same Python fallback validation the CI uses (no kubeconform binary required
  for local runs; kubeconform is used in CI where it's installed).
- If kubeconform is available locally, prefer it for full JSON Schema validation.
- Keep the script simple and self-contained — no extra shell files needed.

---

## Zone 3 — Scoped out

- Installing kubeconform as a pinned tool binary (bin/kubeconform) — not required.
- Windows-compatible Makefile syntax — out of scope.
- Validating non-Pipeline resources (Services, Deployments) — out of scope for this item.
