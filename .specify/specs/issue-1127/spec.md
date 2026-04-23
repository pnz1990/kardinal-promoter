# Spec: issue-1127 — Document multi-tenant workaround

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 7`
- **Implements**: **No multi-tenant project isolation** (🔲 → ✅)

## Zone 1 — Obligations

1. `docs/guides/security.md` contains an expanded "Multi-tenant isolation" section that documents the "one kardinal install per application namespace" workaround with concrete Helm install commands.
2. The section explicitly states the limitation: kardinal has no Project CRD, so Pipelines and Bundles in a shared namespace cannot have per-team RBAC isolation.
3. The section documents the workaround: install kardinal once per team namespace using `--set controller.watchNamespace=<team-namespace>`, with a concrete example showing two teams.
4. The section warns that the workaround is costly (multiple controller replicas) and identifies when it is the right choice.
5. `docs/design/15-production-readiness.md` moves the item from `🔲` to `✅` with a PR reference.

## Zone 2 — Implementer's judgment

- Where to add: expand the existing "Multi-tenant isolation" section in `docs/guides/security.md` (already at line ~172).
- Depth: practical guide with Helm commands, not architectural explanation.
- Length: ~40-60 lines — enough to be useful, not a wall of text.

## Zone 3 — Scoped out

- Not implementing a Project CRD (that is a future feature).
- Not adding namespace-scoped controller mode (already implemented per design doc).
- Not documenting RBAC for non-Kubernetes auth systems.
