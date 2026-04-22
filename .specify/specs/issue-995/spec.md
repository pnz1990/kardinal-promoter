# Spec: issue-995 — Document zero-downtime SCM credential rotation

## Design reference
- **Design doc**: N/A — pure documentation item (no user-visible behavior change to code)
- **Section**: Customer doc update for PR #994 feature
- **Implements**: Document `--scm-token-secret-name` zero-downtime rotation in `docs/quickstart.md` and `docs/scm-providers.md`

---

## Zone 1 — Obligations (falsifiable)

1. **O1**: `docs/quickstart.md` must explain that `github.secretRef.name` enables automatic token reload at runtime without a controller restart. A user reading the quickstart must understand they do not need to restart on rotation.

2. **O2**: `docs/scm-providers.md` must include a dedicated section titled **Credential rotation (zero-downtime)** that explains:
   - The three new flags (`--scm-token-secret-name`, `--scm-token-secret-namespace`, `--scm-token-secret-key`)
   - The 30-second polling interval
   - How to rotate a PAT: update the Secret, no controller restart needed
   - The atomic swap guarantee (concurrent promotions not disrupted)

3. **O3**: Both docs must be accurate relative to the implementation in `pkg/scm/dynamic.go` and `pkg/scm/secret_watcher.go`. No inaccurate claims.

4. **O4**: The quickstart credential section must show `github.secretRef.name` as **Option A (recommended for production)** and direct token as **Option B (dev/testing only)** — matching the existing structure and reinforcing the recommended path.

---

## Zone 2 — Implementer's judgment

- Whether to create a new `docs/credential-rotation.md` or add a section to `docs/scm-providers.md` (prefer adding a section to `scm-providers.md` to avoid doc sprawl)
- Exact wording of the rotation steps
- Whether to mention the `PKG_NAMESPACE → kardinal-system` fallback for `--scm-token-secret-namespace`

---

## Zone 3 — Scoped out

- Adding new flags documentation to `docs/cli-reference.md` (controller flags, not CLI)
- Changes to any Go source code
- Documentation of webhook secrets (out of scope of this rotation)
