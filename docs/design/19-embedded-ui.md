# 19: Embedded React UI

> Status: Complete | Created: 2026-04-22
> See also: `web/src/`, `web/embed.go`

---

## What this does

Embeds a React 19 dashboard into the controller binary using `go:embed`. The UI shows Pipeline state, Bundle promotion progress, PolicyGate details, and promotion history. No separate deployment required.

---

## Present (✅)

- ✅ **React 19 + Vite + TypeScript**: `web/src/` built with `npm run build` into `web/dist/`.
- ✅ **`go:embed all:dist`**: `web/embed.go` embeds the compiled assets into the controller binary.
- ✅ **Pipeline list view**: shows all Pipelines with Bundle phase per environment. Refreshes every 5s.
- ✅ **Bundle detail view**: per-Bundle promotion timeline with step status, gate results, PR links.
- ✅ **PolicyGate panel**: shows gate expression (CEL), last evaluation result, and timestamp.
- ✅ **`/api/v1/` REST endpoints**: `GET /api/v1/pipelines`, `GET /api/v1/bundles`, `GET /api/v1/policy-gates`. Used by both UI and CLI.
- ✅ **UI auth**: configurable token-based auth via `pkg/uiauth`. Disabled by default in dev mode.
- ✅ **`usePolling.ts`**: 5s polling with staleness indicator ("refreshed X ago").

---

## Future (🔲)

- 🔲 **DAG visualization**: interactive node graph for the promotion DAG. Inspired by kro-ui DAG component.
- 🔲 **6-state health chips**: Ready/Degraded/Reconciling/Pending/Error/Unknown per kro-ui pattern.
- 🔲 **CEL expression highlighting**: syntax-highlighted CEL in the PolicyGate panel.

---

## Zone 1 — Obligations

**O1** — The UI is accessible at `http://<controller-host>/ui/` without installing anything separately.
**O2** — API endpoints return JSON with Content-Type: application/json.
**O3** — The embedded binary size does not exceed 100MB (enforced by release CI).
