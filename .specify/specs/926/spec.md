# Spec: docs security guide UI API auth (Issue #926)

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future — UI authentication gaps`
- **Implements**: In-cluster kubectl port-forward UX doc + security guide entry (docs companion to #924)

---

## Zone 1 — Obligations (falsifiable)

**O1** — `docs/guides/security.md` MUST have a section covering `/api/v1/ui/*` authentication
with the `--ui-auth-token` / `KARDINAL_UI_TOKEN` flag shipped in PR #924.

**O2** — The section MUST document the recommended access method (kubectl port-forward)
for production clusters without TLS configured.

**O3** — The section MUST include a table distinguishing open paths (`/ui/*`) from
protected paths (`/api/v1/ui/*`).

---

## Zone 2 — Implementer's judgment

- Add a new `## UI API Access Control` section before `## Further Reading`.
- Include example Helm values and env var usage.
- Cross-link issues #911 (TLS) and #913 (port-forward UX).

---

## Zone 3 — Scoped out

- Adding `controller.uiAuthToken` to the Helm chart values.yaml (separate enhancement).
- TLS configuration (issue #911).
- CORS configuration (issue #912).
