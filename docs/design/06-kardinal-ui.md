# 06: kardinal-ui

> Status: Active
> Depends on: CRD types
> Blocks: nothing (can be built in parallel with the controller)

## Purpose

kardinal-ui is an embedded web UI served by the kardinal-controller binary. It renders the promotion DAG with per-node state, PolicyGate evaluations, Bundle provenance, and PR links. It can also write: create a Bundle, promote or roll back an environment, and pause or resume a Pipeline (see [Interaction Model](#interaction-model)). Authentication is off unless a flag enables it (see [Authentication](#authentication)).

## Technical Stack

- **Frontend:** React 19 + TypeScript + Vite
- **Bundling:** Static assets built to `web/dist/`, embedded in the Go binary via `go:embed`
- **Serving:** Go `net/http` handler at `/ui/` on its own listener (`--ui-listen-address`, default `:8082`)
- **Data:** Reads Kubernetes CRDs via a backend API proxy. No direct browser-to-API-server connection (avoids CORS and auth complexity).
- **Architecture:** Same pattern as kro-ui. Single binary, no separate frontend deployment.

## Go Package Structure

```
web/
  embed.go              # go:embed all:dist → web.Assets
  dist/                 # built frontend (index.html, JS, CSS), committed; rebuilt by `make ui`
  src/
    main.tsx            # React entry point
    App.tsx             # layout, pipeline and node selection, polling
    api/client.ts       # /api/v1/ui client: bearer token, timeouts
    types.ts            # TypeScript types matching the API JSON
    usePolling.ts       # polling hook (also useUrlState.ts, useKeyboardShortcuts.ts, ...)
    components/
      DAGView.tsx       # DAG renderer (dagre layout + SVG)
      PipelineList.tsx  # all Pipelines with the current Bundle per environment
      PipelineLaneView.tsx # stage lanes with promote / roll back actions
      NodeDetail.tsx    # side panel: step details, PR link, provenance, gate CEL
      BundleTimeline.tsx # Bundle history and compare
      PolicyGatesPanel.tsx # gates with expression and result
      ActionBar.tsx     # pause / resume
      HealthChip.tsx    # health status indicator
  test/e2e/journeys/    # Playwright journeys (001-011)
```

## Backend API Proxy

The controller exposes a REST API at `/api/v1/ui/` that proxies CRD reads from the Kubernetes API server. This avoids requiring the browser to authenticate directly to the K8s API.

| Endpoint | Returns | Source CRD |
|---|---|---|
| `GET /api/v1/ui/pipelines` | All Pipelines with the active Bundle and blocker/failed-step counts | Pipeline + Bundle + PolicyGate + PromotionStep |
| `GET /api/v1/ui/pipelines/{name}/bundles` | Bundle history for a Pipeline | Bundle |
| `GET /api/v1/ui/bundles/{name}/graph[?namespace=]` | Promotion DAG for one Bundle: one node per environment and per gate instance, edges from the Pipeline's dependencies (waves, `dependsOn`, else list order). Gate nodes carry `state` and `holding` (as for `/gates`). Steps and gates come from the Bundle's namespace; `namespace` picks the Bundle when names repeat | Pipeline + Bundle + PromotionStep + PolicyGate |
| `GET /api/v1/ui/bundles/{name}/steps[?namespace=]` | PromotionSteps of a Bundle, from the Bundle's namespace; `namespace` picks the Bundle when names repeat | Bundle + PromotionStep |
| `GET /api/v1/ui/gates` | PolicyGates, each with `state` (`Pass`, `Block`, `Superseded`, `Pending` or `Waiting`) and `holding` (the gate holds its Bundle back, `graph.GateHolds`, the rule behind the blocker count). Pipelines, Bundles and PromotionSteps are read only when a gate instance is not ready | PolicyGate, + Pipeline + Bundle + PromotionStep when a gate instance is not ready |
| `GET /api/v1/ui/steps/{namespace}/{name}/events` | Events of that PromotionStep only; `404` when the step does not exist | PromotionStep + Event |
| `POST /api/v1/ui/bundles`, `/promote`, `/rollback` | Create a Bundle (`/rollback` with `hold`: and add the hold to the Pipeline) | Bundle (+ Pipeline with `hold`) |
| `POST /api/v1/ui/release-hold` | Release a rollback hold (spec.holds) | Pipeline |
| `POST /api/v1/ui/approvals` | Record, replace or revoke the TokenReview user's Approval (lifecycle.RecordApproval); 403 without a verified identity | Bundle + Pipeline + Approval |
| `POST /api/v1/ui/pause`, `/resume` | Pause or resume a Pipeline | Pipeline |
| `POST /api/v1/ui/gates/{namespace}/{name}/approve` | Add an override to a gate (retried on write conflicts) | PolicyGate |
| `POST /api/v1/ui/validate-cel` | Check a CEL expression | none |
| `GET /api/v1/openapi.json` | The OpenAPI document of the UI API and the Bundle API ([REST API](../reference/rest-api.md)) | none |

All endpoints return JSON. The controller reads CRDs using its existing Kubernetes client and transforms them into UI-friendly JSON structures (omitting internal fields, resolving references).

Authentication (shared token, or TokenReview plus a SubjectAccessReview for every object read or written) and CORS are described in [the security guide](../guides/security.md#ui-api-access-control). A failed list returns `500` instead of an empty result.

## Data Refresh

The UI polls (fetch every 5 seconds on the active page) rather than using WebSocket or watch, for simplicity. The API has no response cache; each request reads through the controller's client.

Server push (the controller already watches the CRDs with informers, and could broadcast changes to WebSocket clients) is not planned.

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

The frontend is built with `make ui` (`npm ci && npm run build` in `web/`) and the output (`web/dist/`) is embedded in the Go binary:

```go
// web/embed.go
package web

import "embed"

//go:embed all:dist
var Assets embed.FS
```

`cmd/kardinal-controller/main.go` passes `fs.Sub(web.Assets, "dist")` to `newUIHandler` (`cmd/kardinal-controller/ui_auth.go`). That builds a `net/http` ServeMux with:

- `/api/v1/ui/*`, registered by `RegisterRoutes` in `cmd/kardinal-controller/ui_api.go`
- `/ui/`, an `http.FileServer` over the assets that serves files and `index.html` but never lists a directory

There is no SPA fallback: the UI keeps its state in the URL hash (`#pipeline=<name>&node=<id>`), so every page is `/ui/`. The mux is wrapped, from the inside out, in the auth middleware (when enabled), a request body limit, the CORS and Host check, and security headers (CSP, anti-framing, nosniff).

The SCM webhook and the Bundle API are on a separate ServeMux in `main.go`, on the webhook listener:

- `/webhook/scm` and `/webhook/scm/health`
- `/api/v1/bundles`, only when `--bundle-api-token` is set

## Port Separation

| Listener | Flag | Default | Serves |
|---|---|---|---|
| Metrics | `--metrics-bind-address` | `:8080` | `/metrics` |
| Probes | `--health-probe-bind-address` | `:8081` | `/healthz`, `/readyz` |
| UI | `--ui-listen-address` | `:8082` | `/ui/*`, `/api/v1/ui/*` |
| Webhook | `--webhook-bind-address` | `:8083` | `/webhook/*`, `/api/v1/bundles` |

This allows exposing the UI to browser users (VPN) while restricting the webhook and Bundle API to CI and SCM networks. `--tls-cert-file` and `--tls-key-file` switch both the UI and webhook listeners to TLS.

## Authentication

The UI API has three modes. Static assets at `/ui/*` are public in all of them (they hold no data).

- **Off (default).** The controller logs a startup warning and reads and writes with its own ServiceAccount. `/api/` serves only clients whose TCP peer is loopback (`kubectl port-forward`) and whose request has no proxy header (`Forwarded`, `X-Forwarded-*`, `X-Real-Ip`, `X-Envoy-*`, `l5d-*`); everyone else gets `403` (#1262). To limit DNS rebinding, `/api/` requests are also accepted only when the Host is `localhost`, `127.0.0.1`, `::1` or a name in `--ui-allowed-hosts` (the chart adds the Service DNS names).
- **Static token** (`--ui-auth-token`, env `KARDINAL_UI_TOKEN`). Every `/api/v1/ui/*` request needs `Authorization: Bearer <token>`. It takes precedence over TokenReview.
- **TokenReview** (`--ui-tokenreview-auth`, chart `ui.auth.tokenReview`, default `false`). Bearer tokens are checked with a TokenReview, and every object the API reads or writes is authorized with a SubjectAccessReview for the caller. It fails closed.

The web client asks for a token on `401` and keeps it in `sessionStorage`. CORS is same-origin unless `--cors-allowed-origins` is set. OIDC is not implemented. User setup is in [the security guide](../guides/security.md#ui-api-access-control).

## Interaction Model

The UI provides both read and write operations:

**Read operations** — DAG view, pipeline list, bundle timeline, policy gate expressions, health status.

**Write operations**:
- Pause/resume a pipeline (ActionBar, PR #482)
- Promote an environment, or roll it back to its previous verified version (stage lane and node detail panel, one shared confirmation dialog)

Policy gate overrides are not in the UI; use `kardinal override` (CLI).

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
8. `/api/v1/ui/bundles/{name}/graph` returns nodes with status.
9. `/api/v1/ui/bundles/{name}/steps` returns the steps with their evidence.
10. Static assets: `/ui/` serves `index.html` and never lists a directory.

---

## Present (✅)

The following capabilities are implemented and shipped as of v0.9.0:

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
- 🔲 Per-stage bake countdown and override history — the StageDetailPanel from PR #476 was never mounted and was removed; NodeDetail shows the step list
- ✅ In-UI actions: pause and resume (ActionBar), promote and roll back one environment (stage lane, node detail) — PR #482 (2026-04-14). Gate override is CLI-only (`kardinal override`)
- ✅ Bundle promotion timeline: BundleTimeline — the 10 newest bundles colored by phase, plus the selected one; shift-click two bundles to compare them — PR #478, PR #681 (2026-04-14)
- 🔲 Policy gate detail panel with blocking duration and override history — the GateDetailPanel from PR #477 was never mounted and was removed; gate nodes show the highlighted CEL expression in NodeDetail
- ✅ Release efficiency metrics bar: ReleaseMetricsBar — mean time to the last environment, rollback rate and deploy count over the last 10 bundles; hidden until a bundle reaches the last environment — PR #481 (2026-04-14). It also shows the change failure rate and time to restore from `Pipeline.status.deploymentMetrics` once the Pipeline has deployments (#1488)

---

## Future (🔲)

The following capabilities are declared in `docs/aide/vision.md` §F7 (kardinal-ui) but not yet implemented:

*All epic #587 items are now complete.*

### UI authentication gaps (competitive/security pressure — 2026-04-20)

When this section was written, the embedded UI (`cmd/kardinal-controller/ui_api.go`) served all endpoints with **no authentication**. With no auth mode set, `/api/` now answers only loopback clients (see [Authentication](#authentication)); the items below added the opt-in modes. The UI listen address (`:8082`) binds all interfaces. A platform team at a Series B company would fail this in a security review on day one.

- ✅ **UI API authentication** — `--ui-auth-token` flag (env: `KARDINAL_UI_TOKEN`) added to `main.go`. When set, all `/api/v1/ui/*` routes require `Authorization: Bearer <token>`. Static `/ui/*` assets bypass auth. Constant-time comparison via `crypto/subtle`. Implemented in PR #909. With no auth mode set, `/api/` now answers only loopback clients (`kubectl port-forward`); other clients get 403 (#1262).
- ✅ **TLS for UI and webhook HTTP servers** — `--tls-cert-file` / `--tls-key-file` flags (env: `KARDINAL_TLS_CERT_FILE` / `KARDINAL_TLS_KEY_FILE`) added to `main.go`. When both are set, `http.ListenAndServeTLS` is used for both the UI server (`:8082`) and webhook server (`:8083`). Falls back to plain HTTP when neither is set (backwards compatible). Helm values `controller.tlsCertFile` and `controller.tlsKeyFile` support cert-manager volume mount pattern. Implemented in PR #911. **Update (audit remediation, 2026-09-29):** `listenAndServeWithTLS()` is replaced by `httpServer` (`cmd/kardinal-controller/http_server.go`), a manager Runnable: both servers start after the informer caches sync, have read/write/idle timeouts, drain in-flight requests on shutdown, and a bind or serve error stops the controller instead of only being logged. Setting only one of the two TLS flags is now a startup error instead of a silent fallback to plain HTTP.
- ✅ **CORS lockdown for UI API** — `--cors-allowed-origins` flag (env: `KARDINAL_CORS_ORIGINS`) added to `main.go`. Default (empty): same-origin only — cross-origin requests to `/api/v1/ui/*` are rejected with 403. Set to an explicit comma-separated list to allow specific origins. Set to `*` to allow all origins (development opt-out). CORS headers are only applied to `/api/v1/ui/*`; static `/ui/*` assets and webhook routes are unaffected. Implemented in PR #912.
- ✅ **In-cluster `kubectl port-forward` UX** — `InsecureConnectionBanner` component renders a dismissible amber warning when the UI is accessed over plain HTTP from a non-localhost address. Loopback (`localhost`, `127.0.0.1`, `[::1]`) is exempt since port-forward to localhost is the documented access method. `docs/installation.md` now has an "Accessing the UI" section documenting `kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082` (the chart Service exposes the UI port as `ui`). Implemented in PR #913.
- ✅ **Kubernetes TokenReview-based auth for UI API** (PR #1015, 2026-04-21) — `--ui-tokenreview-auth` flag (env: `KARDINAL_UI_TOKENREVIEW_AUTH=true`) added. When enabled and `--ui-auth-token` is not set, the UI API authenticates bearer tokens via `authenticationv1.TokenReview`. Cluster-native authentication: users authenticate with their kubeconfig tokens. Static `--ui-auth-token` takes precedence when both are set. Fail-closed: TokenReview API failure → 503. Implementation in `pkg/uiauth/tokenreview.go` with testable `TokenReviewer` interface. **Update (audit remediation, 2026-09-29):** TokenReview mode now also authorizes: every object the UI API reads or writes for the caller is checked with a `SubjectAccessReview` (`pkg/uiauth/access.go`, `AuthorizingClient`), so a user cannot see or change more through the UI than through `kubectl`. Denied → 403, review API failure → 503, and the controller exits at startup if the review clients cannot be built (previously it logged and served an open UI). Results are cached for 30s per token and action. The web client now sends the token (kept in `sessionStorage`) and shows a sign-in dialog on 401; before this, enabling either auth mode made the UI unusable. User RBAC is documented in `docs/guides/security.md` §UI API Access Control.
---

## Enterprise polish design (added 2026-04-17)

This section documents design decisions made during the epic #587 UI overhaul.
It was written after the work, as the design layer for the remaining 🔲 Future items.

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

The axe-core Playwright check runs in CI as a separate test file (`web/test/e2e/journeys/009-accessibility.spec.ts`).
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
