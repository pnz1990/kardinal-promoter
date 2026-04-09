# kardinal-promoter Constitution

## Core Principles

### XI. Autonomous Development is Non-Negotiable
This project is built 100% by autonomous AI agents. No human writes code, specs, or tests. Agents work in parallel in isolated git worktrees, validate each other's work via PR review, and work backwards from user documentation and examples. If a feature is not described in `docs/`, `examples/`, or `.specify/specs/`, it does not exist. The implementation serves the documentation, not the other way around. Any agent that skips tests, marks tasks complete without implementation, or deviates from the spec without escalating is in violation of this principle.

### I. Kubernetes is the Control Plane
Every object in the system is a Kubernetes CRD. etcd is the database. The API server is the API. There is no external database, no dedicated API server, and no state outside of etcd. CLI, UI, and webhook endpoints are convenience layers that create and read CRDs. A user can operate the entire system with kubectl.

### II. Graph is the Execution Engine
Every promotion pipeline runs as a kro Graph internally. The Graph controller handles DAG ordering, parallel execution, conditional inclusion, and teardown. kardinal-promoter generates Graph specs and reconciles the child CRDs that Graph creates. The controller never reimplements DAG logic.

### III. Never Mutate Workload Resources
The controller never creates, updates, or deletes Deployments, Services, HTTPRoutes, or any workload resource. All cluster state changes flow through Git. In-cluster progressive delivery is delegated to Argo Rollouts or Flagger. Health verification is read-only.

### IV. Pluggable by Default
Every integration point (SCM providers, manifest update strategies, health verification, delivery delegation, metric providers, artifact sources, promotion steps) is a Go interface. Phase 1 ships one implementation per interface. Adding a provider means implementing the interface and registering it. No refactoring of core logic.

### V. Policies are DAG Nodes
PolicyGates are nodes in the Graph, not a separate evaluation engine. They are visible in the UI, inspectable via CLI, and block downstream steps until their CEL expression evaluates to true. Org-level gates are injected as mandatory DAG dependencies that teams cannot remove.

### VI. PR is the Approval Surface
Every promotion (forward or rollback) that requires human approval produces a Git pull request. The PR body contains promotion evidence: artifact provenance, upstream verification results, and policy gate compliance. Human approval happens by merging the PR.

### VII. Go Code Standards
- Apache 2.0 copyright header on every .go file
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Logging: zerolog via `zerolog.Ctx(ctx)` with structured fields
- Tests: table-driven, `testify/assert` + `require`, `go test -race`
- Commits: Conventional Commits `type(scope): message`
- No `util.go`, no `helpers.go`, no `common.go`
- Every reconciler must be idempotent (safe to re-run after crash)

### VIII. Testing Standards
- Unit tests for every reconciler, step, adapter, and translator function
- Integration tests require a running Graph controller (envtest or kind)
- E2E tests require: kind cluster + Graph controller + GitHub repo + Pipeline + PolicyGate blocking
- Tests must pass with `go test -race`
- Test coverage is not a metric; test quality (edge cases, failure paths) is

### IX. User Documentation First
User documentation and examples are the acceptance criteria. The implementation works backwards from docs/quickstart.md, docs/concepts.md, and the examples/ directory. If a documented behavior does not work, the implementation is wrong, not the documentation.

### X. Single Binary (Standalone Mode)
In standalone mode, kardinal-promoter ships as a single Go binary containing the controller, all reconcilers, the embedded UI, and all HTTP endpoints. No sidecar, no separate API server, no external dependencies beyond kro's Graph controller.

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
