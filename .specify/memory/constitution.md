# kardinal-promoter Constitution

## Core Principles

### XI. Autonomous Development is Non-Negotiable
This project is built 100% by autonomous AI agents. No human writes code, specs, or tests. Agents work in parallel in isolated git worktrees, validate each other's work via PR review, and work backwards from user documentation and examples. If a feature is not described in `docs/`, `examples/`, or `.specify/specs/`, it does not exist. The implementation serves the documentation, not the other way around. Any agent that skips tests, marks tasks complete without implementation, or deviates from the spec without escalating is in violation of this principle.

### XII. The Definition of Done is the North Star
The project is complete when all 5 journeys in `docs/aide/definition-of-done.md` pass end-to-end. Unit tests are necessary but not sufficient. A feature is done when its journey test passes — not when its spec is satisfied in isolation. Every agent reads `docs/aide/definition-of-done.md` before starting any work. Every implementation decision is made in service of making a journey pass.

## Architecture Reference

- Umbrella design: `docs/design/design-v2.1.md`
- Implementation specs: `docs/design/01-graph-integration.md` through `09-config-only-promotions.md`
- User documentation: `docs/quickstart.md`, `docs/concepts.md`, and related docs
- Examples: `examples/quickstart/`, `examples/multi-cluster-fleet/`

## Key Dependencies

- kro Graph primitive: `kro.run/v1alpha1/Graph` (experimental, [ellistarn/kro/tree/krocodile/experimental](https://github.com/ellistarn/kro/tree/krocodile/experimental))
- CEL: `google/cel-go` for PolicyGate expressions
- Go 1.23+, controller-runtime, dynamic client
- React 19 + Vite for kardinal-ui (embedded via `go:embed`)

## Spec Inventory

| Spec | What | Status |
|---|---|---|
| `001-graph-integration` | Graph CRD client, dependency edges, testing | Pending |
| `002-pipeline-translator` | Pipeline to Graph translation with PolicyGate injection | Pending |
| `003-promotionstep-reconciler` | PromotionStep state machine, step execution, evidence | Pending |
| `004-policygate-reconciler` | CEL evaluation, timer recheck, context building | Pending |
| `005-health-adapters` | Deployment, Argo CD, Flux health verification | Pending |
| `006-kardinal-ui` | Embedded React UI with DAG rendering | Pending |
| `007-distributed-architecture` | Controller/agent split, shard routing | Pending |
| `008-promotion-steps-engine` | Step interface, built-in steps, custom webhooks | Pending |
| `009-config-only-promotions` | Config Bundles, config-merge step, Git Subscription | Pending |
