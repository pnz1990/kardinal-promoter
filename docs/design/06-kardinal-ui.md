# 06: kardinal-ui

> Status: Outline
> Depends on: CRD types (for data model)
> Blocks: nothing (can be built in parallel with controller)

Embedded React frontend. Read-only. Renders the promotion DAG with policy gate nodes.

## Scope

- Data model
  - Which CRDs: Graph, PromotionStep, PolicyGate, Bundle, Pipeline
  - Which fields per CRD: status.state, status.ready, status.prURL, status.evidence, spec.environment, spec.expression, etc.
  - How often refreshed: Kubernetes watch (via websocket proxy from controller) or periodic fetch (polling from browser)

- DAG rendering
  - Layout algorithm: how to position nodes (dagre? elkjs? custom?)
  - Node types: PromotionStep (environment box) vs PolicyGate (gate diamond/hexagon)
  - Node states: color coding (green=verified, amber=promoting/pending, red=failed, gray=waiting)
  - Edge rendering: solid for data dependencies, style for gate dependencies
  - Fan-out layout: parallel nodes side by side

- Views
  - Pipeline list: all Pipelines with current Bundle per environment (like `kardinal get pipelines` output)
  - Pipeline detail: the DAG view for a specific Bundle promotion
  - Bundle history: list of Bundles with provenance and per-environment status
  - Node detail panel: click a node to see PromotionStep or PolicyGate detail (PR URL, CEL expression, evidence)

- Real-time updates
  - Option A: controller proxies Kubernetes watch events via websocket to the browser
  - Option B: browser fetches CRD data every N seconds via the controller's API
  - Tradeoff: websocket is real-time but more complex; polling is simpler but has latency

- Embedded architecture
  - React app built to static assets (Vite or similar)
  - Bundled into Go binary via go:embed
  - Served at /ui by the controller's HTTP server
  - --ui-listen-address flag for separate port
  - No authentication (relies on Kubernetes RBAC for CRD access; the controller reads CRDs on behalf of the UI)

- Interaction model
  - Read-only: no mutations from the UI
  - Click node to expand details
  - Link to GitHub PR from PromotionStep node
  - Link to Bundle from any node
  - Filter by Pipeline
