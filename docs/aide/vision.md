# kardinal-promoter: Vision

> Created: 2026-04-09
> Status: Active
> License: Apache 2.0

What the product is for and what it must stay. How to work on the repo is in `AGENTS.md`;
what is planned is in `docs/roadmap.md`; how it compares with other tools is in
`docs/comparison.md`.

## Project Overview

kardinal-promoter is a Kubernetes-native promotion controller that moves versioned artifact bundles through environment pipelines using Git pull requests as the approval mechanism, with policy gates expressed as CEL and represented as visible nodes in the promotion DAG.

The execution engine is kro's Graph primitive, a general-purpose Kubernetes DAG reconciler. Graph handles dependency ordering, parallel execution, conditional inclusion, and teardown. kardinal-promoter handles the promotion-specific logic: Git writes, PR lifecycle, policy evaluation, health verification, and delivery delegation.

All state lives in Kubernetes CRDs. There is no external database, no dedicated API server, and no state outside of etcd. The CLI, UI, and webhook endpoints create and read CRDs. A user can operate the entire system with kubectl.

### Why this project exists

The Kubernetes promotion landscape has an orchestration gap. GitOps tools (Argo CD, Flux) synchronize a single cluster with Git, but they do not understand the relationship between environments. Moving an artifact from dev to staging to prod falls back on CI scripts, manual interventions, or tools that lock you into a specific ecosystem.

Kargo (by Akuity) fills this gap but requires Argo CD, introduces 6+ concepts, and stores state in a dedicated API server. GitOps Promoter fills it with PR-native approval but lacks artifact bundling, DAG pipelines, and governance. No tool combines DAG-structured pipelines, PR-native approval with evidence, visible policy gates, and GitOps-tool agnosticism into a single declarative system.

### Relationship to kro

kro's Graph primitive (`kro.run/v1alpha1/Graph`) is the core DAG engine. Within the kro ecosystem:

- **Graph** is the core DAG primitive. Creates, reconciles, and tears down Kubernetes resources in dependency order.
- **RGD** (ResourceGraphDefinition) is a higher-level concept built on Graph for resource composition.
- **kardinal-promoter** is a separate concept built on Graph for promotion orchestration and policy gating.

Building on Graph directly (rather than on RGD) avoids a translation shim. The controller generates a Graph spec whose nodes are exactly the PromotionStep and PolicyGate CRDs that kardinal-promoter needs. No intermediate abstraction, no unused resource-composition semantics.

kro's Graph kind is alpha (`GraphKind` feature gate), and breaking changes can land between
releases. kro is installed separately; the pinned version is `KRO_VERSION` in
`hack/install-kro.sh`. How to track, upgrade and contribute upstream is in AGENTS.md §kro Upgrade
Cadence; known Graph gaps are in `docs/design/16-graph-capability-ledger.md`.

Reference: [kubernetes-sigs/kro](https://github.com/kubernetes-sigs/kro) — [Graph overview](https://kro.run/next/docs/concepts/graph/overview/)

### Graph-First: The Core Architectural Commitment

**The world is a DAG. Everything in kardinal-promoter is a derivation of the kro Graph primitive.**

This is not aspirational. It is the governing constraint on every implementation decision.
See `docs/design/10-graph-first-architecture.md` for the full decision record.

The layer model:
```
L1: kro Graph API            — universal DAG primitive, CEL evaluation
L2: kardinal APIs             — PromotionStep, PolicyGate, Bundle, Pipeline CRDs
                                All expressed as Graph Watch or Owned nodes
L3: kardinal customer APIs    — Pipeline and PolicyGate definitions
```

**If a feature cannot be expressed as a Graph node** (Watch node, Owned node, or CEL
extension on the Graph environment), that is a signal kro is missing a primitive
that should be contributed upstream. Stop and ask the owner. No logic may leak outside the
Graph layer without the owner's approval.

**Accepted exception (must not grow):** `pkg/cel` — the CEL library that the PolicyGate
reconciler (and `kardinal policy simulate`, through that package) uses to evaluate gate
expressions. It is ledger entry G8 in `docs/design/16-graph-capability-ledger.md` and
§Known Exceptions in `docs/design/10-graph-first-architecture.md`. ScheduleClock already
gives time-based gates their re-evaluation trigger.

## Goals and Objectives

1. Provide a declarative, Kubernetes-native promotion system where every object is a CRD and every state transition is observable via kubectl.
2. Make every promotion a Git pull request with rich evidence: artifact provenance, upstream verification, and policy gate compliance.
3. Represent policy gates as visible nodes in the promotion DAG, inspectable via CLI (`kardinal explain`) and UI, with org-level gates that teams cannot bypass.
4. Support DAG-structured pipelines (parallel fan-out, conditional steps, multi-service dependencies) via kro's Graph primitive.
5. Work with existing GitOps tools (Argo CD, Flux) via pluggable health adapters. Also work without a GitOps tool.
6. Support multi-cluster promotion through the GitOps tool (for example Argo CD hub-spoke), with parallel fan-out to several prod clusters.
7. Support both image promotions (new container versions) and config-only promotions (resource limits, env vars, feature flags) through the same pipeline.

## Target Users

### Platform engineers (primary)

Define promotion pipelines and PolicyGates for their organization. Configure health adapters and Git providers. Manage org-level policies that application teams inherit.

### Application developers (secondary)

Create Bundles (via CI webhook, CLI or UI) to trigger promotions. Review and merge promotion PRs. Use `kardinal explain` to understand why a promotion is blocked. Use the UI to monitor promotion progress.

### SREs and operators (secondary)

Use rollback and pause during incidents, from the CLI or the UI. Monitor promotion health via the UI and controller metrics. Override a gate with a mandatory reason from the CLI (`kardinal override`); the UI does not override gates.

## Core Features

### F1: Pipeline CRD

A user-facing CRD that defines the promotion path for one application. Lists environments in order with Git configuration. The controller translates this into a kro Graph, injecting PolicyGate nodes.

Environments promote sequentially by default. For parallel fan-out, use the `dependsOn` field. Each environment specifies approval mode (`auto` or `pr-review`), update strategy (`kustomize` or `helm`) and health adapter.

### F2: Bundle CRD

An immutable, versioned snapshot of what to deploy. Types: `image` (container images), `config` (Git commit with configuration changes), `mixed` (both). Carries build provenance (commit SHA, CI run URL, author, digest).

Created by CI webhook, CLI, UI, kubectl apply, or Subscription CRD. Includes intent (target environment, skip list with permission enforcement).

Phases: Available, Promoting, Verified, Failed, Superseded. Per-environment evidence (metrics, gate results, approver, timing) is stored in Bundle status for durable audit.

### F3: PolicyGate CRD

CEL-powered policy checks represented as nodes in the promotion Graph. Platform teams define org-level gates (in `platform-policies` namespace) that are automatically injected into every Pipeline targeting matching environments. Teams can add their own gates but cannot remove org gates.

The CEL context (bundle, schedule, environment, metrics, upstream soak time, change windows) is listed in `docs/reference/cel-context.md`.

Re-evaluation via `recheckInterval` and ScheduleClock ticks for time-based gates. A PromotionStep starts only when every required gate is ready and was evaluated at or after the step was created. A started step is not stopped. `spec.when` is deprecated and has no effect.

SkipPermission gates control whether `intent.skip` is allowed on gated environments.

### F4: Promotion Steps Engine

A fixed step sequence per environment, inferred from the update strategy and approval mode.

Built-in steps: `git-clone`, `kustomize-set-image`, `helm-set-image`, `argocd-set-image`, `kustomize-build`, `config-merge`, `git-commit`, `git-push`, `open-pr`, `wait-for-merge`, `health-check`.

A Pipeline cannot define its own steps: `spec.environments[].steps` and `promotionTemplate` are rejected (#1282). An unregistered step name is an error.

### F5: Health Adapters

Pluggable health verification: Deployment condition (`resource`), Argo CD Application health+sync (`argocd`), Flux Kustomization Ready (`flux`), Argo Rollouts (`argoRollouts`), Flagger (`flagger`).

Health is read from the cluster the controller runs in. Remote-cluster health through kubeconfig Secrets is not implemented; multi-cluster setups read health from the hub (for example Argo CD Applications).

### F6: PR Evidence

For `pr-review` environments, the controller opens a PR with structured body: policy gate compliance table, artifact provenance with links, upstream verification timestamps, commit range diff. Labels for filtering (`kardinal`, `kardinal/promotion`, `kardinal/rollback`). Merge detection via webhook with startup reconciliation fallback.

### F7: kardinal-ui

Embedded React UI served by the controller binary via `go:embed`. Renders the promotion DAG with per-node state (PromotionStep: green/amber/red; PolicyGate: pass/fail/pending). Shows Bundle provenance, PR links, policy evaluation details and history. Backend API at `/api/v1/ui/` reads and writes CRDs through the Kubernetes API server.

The UI is not read-only. It can create a Bundle, promote, pause, resume and roll back. With `ui.auth.tokenReview` on, the UI API authenticates each user's bearer token (TokenReview) and checks every object it reads or writes for that user with a SubjectAccessReview, so a user cannot do more through the UI than through kubectl. Auth is off by default. The UI API also has a gate-approve endpoint that the web UI does not use; gate override is CLI-only.

### F8: CLI

Single static Go binary. Commands include `init`, `get pipelines/steps/bundles/subscriptions/auditevents`, `create bundle`, `promote`, `explain` (with `--watch`), `rollback`, `pause`, `resume`, `override`, `history`, `policy list/test/simulate`, `diff`, `validate`, `doctor`, `version`. All commands create or read CRDs. `rollback --emergency` is being deprecated (#1288).

### F9: Config-Only Promotions

Bundle `type: config` references a Git commit SHA. The `config-merge` step applies changes via cherry-pick or overlay. Config Bundles go through the same Pipeline, PolicyGates, and PR flow. Config and image Bundles coexist independently (different types do not supersede each other).

### F10: Subscription CRD

Declarative registry and Git watcher. Image subscriptions watch OCI registries for new tags. Git subscriptions watch repositories for config changes. Auto-creates Bundles when new artifacts are discovered.

## Technical Architecture

### Stack

- **Language:** Go 1.26+
- **Kubernetes:** controller-runtime, dynamic client, CRDs via kubebuilder
- **DAG engine:** kro Graph primitive (`kro.run/v1alpha1/Graph`)
- **Policy expressions:** CEL via `google/cel-go`, with the library in `pkg/cel` (adapted from kro's)
- **Git:** go-git for clone/commit/push; an SCM provider interface for PRs
- **UI:** React 19, TypeScript, Vite, embedded via `go:embed`
- **CLI:** cobra

### CRDs

| CRD | User-created? | Purpose |
|---|---|---|
| Pipeline | Yes | Promotion topology, Git config, environments |
| Bundle | Yes (via CI/CLI/UI/kubectl) | Artifact snapshot with provenance and intent |
| PolicyGate | Yes (platform/team) | CEL policy check, DAG node template |
| PromotionStep | No (created by Graph) | Per-environment promotion execution state |
| PRStatus | No (created by the open-pr step) | PR state, updated by webhook or polling |
| MetricCheck | Yes (optional) | Prometheus query template |
| ChangeWindow | Yes (optional) | Fleet-wide freeze or allowed window |
| ScheduleClock | Yes (optional) | Periodic tick that re-evaluates time-based gates |
| RollbackPolicy | Yes (optional) | Automatic rollback on failure |
| NotificationHook | Yes (optional) | Outbound notifications on promotion events |
| Subscription | Yes (optional) | Registry/Git watcher that creates Bundles |
| AuditEvent | No (created by reconcilers) | Append-only record of promotion events |
| Graph | No (created by controller) | kro DAG spec (generated per Bundle) |

### Pluggable Interfaces

| Interface | Purpose | Implementations |
|---|---|---|
| `scm.SCMProvider` | PR lifecycle (create, merge, comment) | GitHub, GitLab, Bitbucket, Azure DevOps, Forgejo |
| `scm.GitClient` | Git operations (clone, commit, push) | go-git |
| `health.Adapter` | Health verification | resource, argocd, flux, argoRollouts, flagger |
| `metriccheck.MetricsProvider` | Metric queries | Prometheus |
| `source.Watcher` | Artifact discovery | OCI registry, Git |
| `steps.Step` | Promotion step | built-in steps (F4) |

### Multi-Cluster Model

- Argo CD hub-spoke: controller reads Application health from hub cluster, no cross-cluster API calls
- Parallel fan-out: `dependsOn` on Pipeline environments creates parallel Graph nodes (`examples/multi-cluster-fleet/`)
- Remote kubeconfig health (Flux per-cluster, bare Kubernetes) is not implemented
- Live multi-cluster (J2) evidence is tracked in #1293

### Graph Integration

The controller generates a Graph spec per Bundle. Dependency edges are inferred from CEL `${}` references between node templates. Each PromotionStep carries fields (`upstreamStates`, `requiredGates`) that serve both reconciler logic and edge creation.

Per-Bundle Graph lifecycle: created on Bundle promotion start, owned by Bundle via ownerReferences, cascade-deleted on Bundle GC.

## Non-Functional Requirements

### Performance (targets)

- Controller startup in under 10 seconds
- Pipeline-to-Graph translation in under 1 second
- PolicyGate CEL evaluation in under 10ms
- PR creation in under 5 seconds (GitHub API dependent)
- Health check polling every 10 seconds during HealthChecking state

### Scalability

- Designed for fewer than 50 Pipelines per control plane
- Git rate limits: no polling, webhook-only merge detection with startup reconciliation
- PolicyGate recheck: ~20 writes/minute at 50 pipelines with `recheckInterval: 5m`

### Security

- Pod security: runAsNonRoot, readOnlyRootFilesystem, drop ALL capabilities
- RBAC: minimum required verbs per CRD, org PolicyGates protected by namespace RBAC
- Webhook auth: X-Hub-Signature-256 HMAC (SCM webhooks); Bearer token with a 60 requests/minute limit (Bundle API)
- UI API: optional static token, or TokenReview with a per-user SubjectAccessReview (F7); separate port via `--ui-listen-address`
- Tenancy: the namespace is the tenancy unit (`docs/guides/security.md`)

### Reliability

- All reconcilers are idempotent (safe to re-run after crash)
- Leader election via Kubernetes Lease (`--leader-elect`)
- Graph controller down: existing steps continue, new steps paused
- PR merge detection: webhook primary, startup reconciliation fallback
- PolicyGate staleness: `lastEvaluatedAt` freshness check prevents stale advancement

## Constraints and Assumptions

### Dependencies

- kro's Graph controller must be available in the cluster (alpha, `GraphKind` feature gate, API may change)
- A GitOps tool (Argo CD, Flux, or equivalent) must be syncing from the Git repository
- Bundles come from CI (webhook, CLI or kubectl) or from a Subscription

### Assumptions

- Teams have an existing GitOps repository with Kustomize or Helm per-environment directories
- Environment-specific configuration (secrets, resource limits) is already managed in the GitOps repo
- The Git provider is GitHub, GitLab, Bitbucket, Azure DevOps or Forgejo

### Constraints

- The controller never mutates workload resources (Deployments, Services, etc.)
- CEL is the only expression language (no OPA/Rego, no Cedar)
- Rollback is a forward promotion of a prior Bundle (no separate rollback mechanism)

## Out of Scope

| Excluded | Reason |
|---|---|
| Traffic management (canary weights, blue-green switching) | Delegated to Argo Rollouts or Flagger |
| GitOps sync (cluster reconciliation from Git) | Handled by Argo CD or Flux |
| CI pipelines (building images, running tests) | CI creates Bundles; kardinal-promoter does not build |
| Secret management | Managed in GitOps repo via External Secrets, Sealed Secrets, or SOPS |
| Native canary or blue-green delivery | Delegated |
| External database | Kubernetes is the database |
| Graph primitive development | kro team maintains Graph |
| Formal policy analysis (automated reasoning) | CEL does not support this |

## Reference Documents

| Document | Location |
|---|---|
| Architecture | `docs/architecture.md` |
| Design records | `docs/design/` |
| Public roadmap | `docs/roadmap.md` |
| Comparison with other tools | `docs/comparison.md` |
| Journey definition of done | `docs/aide/definition-of-done.md` |
| User quickstart | `docs/quickstart.md` |
| Core concepts | `docs/concepts.md` |
| CLI reference | `docs/cli-reference.md` |
| Pipeline reference | `docs/pipeline-reference.md` |
| Policy gates guide | `docs/policy-gates.md` |
| CEL context reference | `docs/reference/cel-context.md` |
| Health adapters guide | `docs/health-adapters.md` |
| Rollback guide | `docs/rollback.md` |
| CI integration guide | `docs/ci-integration.md` |
| PR evidence guide | `docs/pr-evidence.md` |
| Troubleshooting | `docs/troubleshooting.md` |
| Quickstart example | `examples/quickstart/` |
| Multi-cluster example | `examples/multi-cluster-fleet/` |
