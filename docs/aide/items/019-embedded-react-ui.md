# Item 019: Embedded React UI — Stage 9

> **Queue**: queue-009
> **Branch**: `019-embedded-react-ui`
> **Depends on**: 014 (health adapters, merged), 013 (PromotionStep reconciler, merged)
> **Dependency mode**: merged
> **Contributes to**: J1 (UI visibility), J5 (CLI — bonus UI)
> **Priority**: HIGH — enables J1 visual confirmation and J5 bonus visibility

---

## Goal

Deliver `kardinal-ui` — a read-only React 19 application embedded in the controller binary
that renders the promotion DAG with per-node state. Users can navigate to the UI in their
browser to see live promotion progress without kubectl.

---

## Deliverables

### 1. React UI project (`web/src/`)

Create `web/src/` with Vite + React 19 + TypeScript project:
- `package.json` with: react@19, typescript, vite, @dagrejs/dagre, reactflow
- `vite.config.ts` with base path `/ui/`
- `src/App.tsx`: root component with Pipeline list sidebar and DAG view
- `src/components/PipelineList.tsx`: list of Pipelines with active Bundle phase badges
- `src/components/DAGView.tsx`: reactflow graph with PromotionStep (green/amber/red) and PolicyGate (green/red/grey) nodes
- `src/components/NodeDetail.tsx`: detail panel on node click — step outputs, gate expression + reason, PR link, provenance
- `src/api/client.ts`: typed fetch wrappers for all backend API endpoints
- `src/types.ts`: TypeScript types matching the Go API response shapes

### 2. Backend API in controller

In `cmd/kardinal-controller/main.go` and a new `cmd/kardinal-controller/ui_api.go`:
- `GET /api/v1/ui/pipelines` — list Pipelines (name, phase, environment count)
- `GET /api/v1/ui/pipelines/{name}/bundles` — list Bundles for a Pipeline (name, phase, type)
- `GET /api/v1/ui/bundles/{name}/graph` — return Graph nodes + edges + statuses (from PromotionStep and PolicyGate status)
- `GET /api/v1/ui/bundles/{name}/steps` — return PromotionStep list with states
- `GET /api/v1/ui/gates` — return PolicyGate list with statuses
- Add `--ui-listen-address` flag (default `:8082`) for the UI server
- Serve `/ui/` path from embedded `web/dist/` via `go:embed`

### 3. `web/embed.go`

```go
//go:build !nouiembed

package web

import "embed"

//go:embed all:dist
var Assets embed.FS
```

### 4. `Makefile` target

```makefile
ui:
    cd web && npm ci && npm run build
```

`make ui && make build` produces a controller binary that serves the embedded UI.

### 5. Unit tests

- `TestUIAPI_ListPipelines`: GET /api/v1/ui/pipelines returns pipeline list JSON
- `TestUIAPI_ListBundles`: GET /api/v1/ui/pipelines/{name}/bundles returns bundle list
- `TestUIAPI_GetSteps`: GET /api/v1/ui/bundles/{name}/steps returns step list

---

## Acceptance Criteria

- [ ] `make ui && make build` produces a controller binary that serves the UI
- [ ] `GET /api/v1/ui/pipelines` returns JSON array of pipelines
- [ ] `GET /api/v1/ui/bundles/{name}/steps` returns PromotionStep states
- [ ] `web/dist/` is embedded in the binary via `go:embed`
- [ ] `--ui-listen-address` flag configures the UI listen address
- [ ] UI API unit tests pass
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new Go files
- [ ] No banned filenames
