# 06: kardinal-ui

> Status: Active
> Depends on: CRD types
> Blocks: nothing (can be built in parallel with the controller)

## Purpose

kardinal-ui is an embedded web UI served by the kardinal-controller binary. It renders the promotion DAG with per-node state, PolicyGate evaluations, Bundle provenance, and PR links. It is read-only; all mutations go through CRDs (CLI, kubectl, webhook).

## Technical Stack

- **Frontend:** React 19 + TypeScript + Vite
- **Bundling:** Static assets built to `web/dist/`, embedded in the Go binary via `go:embed`
- **Serving:** Go HTTP handler at `/ui` (configurable via `--ui-listen-address` for port separation)
- **Data:** Reads Kubernetes CRDs via a backend API proxy. No direct browser-to-API-server connection (avoids CORS and auth complexity).
- **Architecture:** Same pattern as kro-ui. Single binary, no separate frontend deployment.

## Go Package Structure

```
web/
  embed.go              # go:embed all:dist
  dist/                 # built frontend assets (index.html, JS, CSS)
  src/
    main.tsx            # React entry point
    App.tsx             # Router, layout
    pages/
      PipelineList.tsx  # Pipeline overview (all pipelines with current Bundle per env)
      PipelineDetail.tsx # DAG view for a specific Pipeline + Bundle
      BundleList.tsx    # Bundle history for a Pipeline
      BundleDetail.tsx  # Single Bundle with per-environment evidence
    components/
      DAGGraph.tsx      # DAG renderer (nodes + edges)
      DAGNode.tsx       # PromotionStep or PolicyGate node
      NodeDetail.tsx    # Side panel with node details
      HealthBadge.tsx   # Health status indicator
      PolicyGateCard.tsx # Gate expression + evaluation result
      PRLink.tsx        # Link to GitHub/GitLab PR
      BundleProvenance.tsx # Commit, CI run, author display
    lib/
      api.ts            # Backend API client
      types.ts          # TypeScript types matching CRD specs
      dag.ts            # DAG layout computation (dagre)
    hooks/
      usePolling.ts     # Polling hook for data refresh
```

## Backend API Proxy

The controller exposes a REST API at `/api/v1/ui/` that proxies CRD reads from the Kubernetes API server. This avoids requiring the browser to authenticate directly to the K8s API.

| Endpoint | Returns | Source CRD |
|---|---|---|
| `GET /api/v1/ui/pipelines` | All Pipelines with current Bundle status | Pipeline + Bundle CRDs |
| `GET /api/v1/ui/pipelines/:name` | Single Pipeline with environment status | Pipeline + Bundle + PromotionStep CRDs |
| `GET /api/v1/ui/pipelines/:name/graph` | Graph spec + node statuses for current Bundle | Graph + PromotionStep + PolicyGate CRDs |
| `GET /api/v1/ui/pipelines/:name/bundles` | Bundle history for a Pipeline | Bundle CRDs |
| `GET /api/v1/ui/bundles/:name` | Single Bundle with evidence | Bundle CRD |
| `GET /api/v1/ui/policygates` | All PolicyGate templates (not instances) | PolicyGate CRDs in policy namespaces |

All endpoints return JSON. The controller reads CRDs using its existing Kubernetes client and transforms them into UI-friendly JSON structures (omitting internal fields, resolving references).

## Data Refresh

The UI uses polling (fetch every 5 seconds on the active page) rather than WebSocket/watch for simplicity in Phase 1.

Phase 2+ may add WebSocket support for real-time updates:
- Controller watches relevant CRDs via informers (already running for reconciliation)
- On change, broadcast to connected WebSocket clients
- UI receives updates and patches the local state

For Phase 1, 5-second polling is sufficient. The API proxy caches responses for 2 seconds to reduce API server load.

## DAG Rendering

The promotion DAG is rendered using [dagre](https://github.com/dagrejs/dagre) for layout computation and SVG for rendering.

**Node types:**

| CRD | Visual | Color coding |
|---|---|---|
| PromotionStep (Pending) | Rounded rectangle | Gray |
| PromotionStep (Promoting) | Rounded rectangle | Amber |
| PromotionStep (WaitingForMerge) | Rounded rectangle | Amber with PR icon |
| PromotionStep (HealthChecking) | Rounded rectangle | Amber with health icon |
| PromotionStep (Verified) | Rounded rectangle | Green |
| PromotionStep (Failed) | Rounded rectangle | Red |
| PolicyGate (pass) | Hexagon | Green |
| PolicyGate (fail) | Hexagon | Red |
| PolicyGate (pending) | Hexagon | Gray |

**Edges:** Solid lines from upstream to downstream. Arrow direction indicates promotion flow.

**Layout:** Top-to-bottom or left-to-right (user toggle). Dagre handles node positioning. Parallel fan-out nodes are placed side by side.

## Views

### Pipeline List

The landing page. Shows all Pipelines with:
- Pipeline name
- Current Bundle version
- Per-environment status (colored dots: green/amber/red/gray)
- Age of the current promotion

Clicking a Pipeline navigates to the Pipeline Detail view.

### Pipeline Detail (DAG View)

The primary view for monitoring a promotion. Shows:
- The promotion DAG with PromotionStep and PolicyGate nodes
- Per-node status (color + label)
- Clicking a node opens the Node Detail panel

### Node Detail Panel

A side panel that appears when clicking a DAG node. Contents depend on node type:

**PromotionStep:**
- Environment name
- Bundle version
- Current state with timestamp
- PR URL (clickable link to GitHub/GitLab)
- Health adapter type and status details
- Evidence (metrics, gate duration, approver) if Verified
- Failure reason if Failed

**PolicyGate:**
- Gate name and scope (org/team)
- CEL expression (displayed as code)
- Current evaluation result (pass/fail)
- Evaluation reason (e.g., "schedule.isWeekend = false")
- Last evaluated timestamp
- Recheck interval

### Bundle List

Shows Bundle history for a Pipeline:
- Version, images, phase, age
- Per-environment status columns
- Provenance (commit, author, CI run)
- Clickable to Bundle Detail

### Bundle Detail

Single Bundle view with:
- Artifact details (images, digests)
- Provenance (commit link, CI run link, author)
- Intent (target, skip)
- Per-environment status with evidence
- Graph reference (link to DAG view)

## Embedded Architecture

The frontend is built during CI (`bun run build` or `npm run build`) and the output (`web/dist/`) is embedded in the Go binary:

```go
// web/embed.go
package web

import "embed"

//go:embed all:dist
var DistFS embed.FS
```

The controller serves it:

```go
// internal/server/server.go
func (s *Server) setupRoutes() {
    // API routes
    s.router.Route("/api/v1", func(r chi.Router) {
        r.Route("/bundles", s.bundleWebhookRoutes)
        r.Route("/ui", s.uiAPIRoutes)
    })

    // Webhook route
    s.router.Post("/webhooks", s.handleSCMWebhook)

    // Metrics
    s.router.Handle("/metrics", promhttp.Handler())

    // SPA fallback: serve index.html for all unmatched routes under /ui
    s.router.Handle("/ui/*", s.spaHandler())
}

func (s *Server) spaHandler() http.Handler {
    fs, _ := fs.Sub(web.DistFS, "dist")
    fileServer := http.FileServer(http.FS(fs))
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Try to serve the file. If not found, serve index.html (SPA routing).
        path := strings.TrimPrefix(r.URL.Path, "/ui")
        if _, err := fs.Open(path); err != nil {
            r.URL.Path = "/"
        }
        fileServer.ServeHTTP(w, r)
    })
}
```

## Port Separation

Default: all endpoints on `:8080`.

With `--ui-listen-address=:8081`:
- `:8080` serves `/api/v1/bundles`, `/webhooks`, `/metrics`
- `:8081` serves `/ui/*` and `/api/v1/ui/*`

This allows exposing the UI to browser users (VPN) while restricting the API to CI networks (firewall rules on port 8080).

## Authentication

Phase 1: No authentication on the UI. The controller reads CRDs on behalf of all UI users using its own ServiceAccount. This is acceptable when the UI is accessed via VPN or internal network.

Phase 2+: Add optional OIDC authentication. The UI redirects unauthenticated users to an OIDC provider. The controller validates the token and scopes CRD reads to the user's Kubernetes RBAC permissions.

## Interaction Model

The UI provides both read and write operations:

**Read operations** — DAG view, pipeline list, bundle timeline, policy gate expressions, health status.

**Write operations** (via ActionBar, added in PR #482):
- Pause/resume a pipeline
- Rollback a pipeline to a previous bundle
- Override a policy gate (with mandatory reason, creates an AuditEvent)

All mutations go through the backend API proxy which calls the Kubernetes API server.
Direct CRD mutation via CLI (`kardinal pause`, `kardinal rollback`) and kubectl also remain available.

## CSS and Design Tokens

Use CSS custom properties (design tokens) for colors, spacing, and typography. No CSS frameworks (Tailwind, MUI, etc.). Consistent with kro-ui's approach.

Key tokens:
- `--color-status-verified`: green for Verified nodes
- `--color-status-promoting`: amber for in-progress nodes
- `--color-status-failed`: red for Failed nodes
- `--color-status-pending`: gray for Pending nodes
- `--color-gate-pass`: green for passing PolicyGates
- `--color-gate-fail`: red for failing PolicyGates

## Unit Tests

Frontend tests (Vitest):
1. DAG layout: verify node positioning for linear 3-env pipeline.
2. DAG layout: verify parallel nodes for fan-out pipeline.
3. Node coloring: verify correct colors for each state.
4. PolicyGate card: verify expression display and evaluation result.
5. Bundle provenance: verify commit link, CI run link rendering.
6. PR link: verify correct URL from PromotionStep status.

Backend API tests (Go):
7. `/api/v1/ui/pipelines` returns correct structure.
8. `/api/v1/ui/pipelines/:name/graph` returns nodes with status.
9. `/api/v1/ui/bundles/:name` returns evidence.
10. SPA fallback: unmatched routes serve index.html.

---

## Present (✅)

The following capabilities are implemented and shipped as of v0.8.x:

- ✅ Embedded React UI served by controller binary (`go:embed`) — PR #19
- ✅ DAG view: PromotionStep and PolicyGate nodes with per-node state colors — PR #19
- ✅ PipelineList: all pipelines with current Bundle per environment — PR #19
- ✅ NodeDetail side panel: step details, PR links, Bundle provenance — PR #19
- ✅ BundleTimeline: artifact history with diff links — PR #19
- ✅ PolicyGateCard: CEL expression + evaluation result — PR #19
- ✅ Backend API proxy at `/api/v1/ui/` — PR #19
- ✅ CSS design tokens (no framework) — PR #19
- ✅ Dark/light mode: system-aware with manual toggle — PR #734
- ✅ CSS token migration: 206 hardcoded hex values replaced — PR #738
- ✅ URL routing: pipeline + node selection persisted in hash fragment — PR #742
- ✅ Global keyboard shortcuts: `?` (help modal), `r` (refresh), `Esc` (dismiss) — PR #750
- ✅ WCAG 2.1 AA automated check: axe-core in Playwright CI — PR #756
- ✅ WCAG 2.1 AA color contrast: full color system audit, all violations fixed — PR #760/#765
- ✅ Nested-interactive WCAG fix: PipelineLaneView keyboard nav — PR #759
- ✅ Error boundaries on DAGView, PipelineList, NodeDetail, BundleTimeline — PR #755
- ✅ Copy-to-clipboard on pipeline names and bundle hashes — PR #764
- ✅ Stale data indicator: amber→red+pulse escalation after 30s — PR #767
- ✅ Focus trap in keyboard shortcuts modal (Tab/Shift+Tab cycle, return focus on close) — PR #783
- ✅ Skeleton loading states: NodeDetail step details, BundleTimeline chips, PolicyGatesPanel — PR #791
- ✅ `/` keyboard shortcut to focus pipeline search input; Esc clears + blurs; filter always visible — PR #805, 2026-04-19
- ✅ Responsive layout at 1280px width: scrollWidth ≤ 1280 verified by Playwright test — PR #806, 2026-04-19
- ✅ Virtualization for pipeline list with 50+ entries: @tanstack/react-virtual flat-list mode; falls back to normal for multi-namespace grouped display — PR #815, 2026-04-19
- ✅ Fleet-wide health dashboard: FleetHealthBar — blocked pipelines, CI red, interventions scannable in one table — PR #480 (2026-04-14)
- ✅ Per-pipeline operations view: PipelineOpsTable — sortable health columns: inventory age, last merge, blockage time — PR #475 (2026-04-14)
- ✅ Per-stage detail: StageDetailPanel — step list, bake countdown, override history — PR #476 (2026-04-14)
- ✅ In-UI actions: ActionBar — pause, resume, rollback, override gate (with mandatory reason) — PR #482 (2026-04-14)
- ✅ Bundle promotion timeline with rollback records and override audit trail: BundleTimeline + AuditEvents — PR #478, PR #681 (2026-04-14)
- ✅ Policy gate detail panel: GateDetailPanel — CEL highlighting, current variable values, blocking duration, override history — PR #477 (2026-04-14)
- ✅ Release efficiency metrics bar: ReleaseMetricsBar — inline P50/P90 metrics on pipeline detail — PR #481 (2026-04-14)

---

## Future (🔲)

The following capabilities are declared in `docs/aide/vision.md` §F8 but not yet implemented:

*All epic #587 items are now complete.*

### UI authentication gaps (competitive/security pressure — 2026-04-20)

The embedded UI (`cmd/kardinal-controller/ui_api.go`) currently serves all endpoints with **no authentication**. The UI listen address (`:8082`) is bound to all interfaces. A platform team at a Series B company would fail this in a security review on day one.

- ✅ **UI API authentication** — `--ui-auth-token` flag (env: `KARDINAL_UI_TOKEN`) added to `main.go`. When set, all `/api/v1/ui/*` routes require `Authorization: Bearer <token>`. Static `/ui/*` assets bypass auth. Constant-time comparison via `crypto/subtle`. Default is open (no token) for backwards compatibility. Implemented in PR #909.
- ✅ **TLS for UI and webhook HTTP servers** — `--tls-cert-file` / `--tls-key-file` flags (env: `KARDINAL_TLS_CERT_FILE` / `KARDINAL_TLS_KEY_FILE`) added to `main.go`. When both are set, `http.ListenAndServeTLS` is used for both the UI server (`:8082`) and webhook server (`:8083`). Falls back to plain HTTP when neither is set (backwards compatible). Helm values `controller.tlsCertFile` and `controller.tlsKeyFile` support cert-manager volume mount pattern. Implemented in PR #911.
- ✅ **CORS lockdown for UI API** — `--cors-allowed-origins` flag (env: `KARDINAL_CORS_ORIGINS`) added to `main.go`. Default (empty): same-origin only — cross-origin requests to `/api/v1/ui/*` are rejected with 403. Set to an explicit comma-separated list to allow specific origins. Set to `*` to allow all origins (development opt-out). CORS headers are only applied to `/api/v1/ui/*`; static `/ui/*` assets and webhook routes are unaffected. Implemented in PR #912.
- 🔲 **In-cluster `kubectl port-forward` UX** — until full TLS + auth lands, document that the supported access method is `kubectl port-forward svc/kardinal-controller 8082` and add a note to the UI that warns when accessed without HTTPS (`window.location.protocol != 'https:'`).
---

## Enterprise polish design (added 2026-04-17)

This section documents design decisions made during the epic #587 UI overhaul.
It was not written before the work (a DDDD violation — see issue history). It is
written now to serve as the design layer for remaining 🔲 Future items.

### Theme system

Two themes: `light` (default) and `dark`. Theme is stored in `localStorage` and
detected from `prefers-color-scheme` on first load. All color values are CSS custom
properties (`--color-*`). No hardcoded hex values anywhere in the component tree.
Theme toggle in the top-right nav.

### WCAG 2.1 AA requirements

All interactive elements must pass axe-core checks in CI. Specific rules enforced:
- `color-contrast`: minimum 4.5:1 ratio for normal text, 3:1 for large text
- `nested-interactive`: no button inside button or anchor inside button
- `aria-live` regions on status displays that update asynchronously

The axe-core Playwright check runs in CI as a separate test file (`web/tests/a11y.spec.ts`).
New UI PRs that introduce axe violations will fail CI.

### URL routing

Selection state (active pipeline, active node) is persisted in the URL hash fragment
using a custom `useUrlState` hook. Format: `#pipeline=<name>&node=<id>`. This allows
sharing links to specific pipeline states and restoring selection on page reload.

### Keyboard navigation

All shortcuts are suppressed when `document.activeElement` is an input, textarea,
select, or contenteditable element. Shortcuts are registered in a single
`useKeyboardShortcuts` hook on `App.tsx`. The `?` key opens a `KeyboardShortcutsPanel`
modal listing all available shortcuts.
