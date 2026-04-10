# kardinal-promoter: Vision

> Created: 2026-04-09
> Status: Active
> License: Apache 2.0

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

Reference: [ellistarn/kro — krocodile branch](https://github.com/ellistarn/kro/tree/krocodile/experimental)

### kro Tracking and Contribution Policy

The krocodile/experimental branch is under active development. The Graph API and semantics are evolving continuously (20+ commits/day observed in early April 2026). Every engineer and coordinator must:

1. **Check the krocodile git log before implementing any Graph integration.** Run:
   ```bash
   gh api 'repos/ellistarn/kro/commits?sha=krocodile&per_page=20' \
     --jq '.[] | {sha: .sha[0:8], message: .commit.message[0:80], date: .commit.committer.date}'
   ```
   Look for changes to `experimental/docs/design/`, `experimental/crds/`, and `experimental/controller/`.

2. **Read the design docs before implementing.** The canonical source of truth for Graph semantics is `experimental/docs/design/` in the krocodile branch, not our own docs. When they disagree, the krocodile docs win.

3. **Contribute upstream rather than work around.** If Graph does not support a capability that kardinal-promoter needs (e.g., native `recheckAfter`, explicit `dependsOn`), open a PR to krocodile first. A contribution that lands upstream eliminates a workaround from our codebase. Workarounds are accepted only when a contribution would block progress for more than one sprint.

4. **Pin the Graph CRD version in our Helm chart and test infrastructure.** Install krocodile at a specific commit in all test clusters (CI and local kind) to catch breakage early. When krocodile updates break our tests, file a GitHub issue with the breaking change before it blocks a sprint.

5. **Update our design docs when Graph semantics change.** The sections most likely to drift are design-v2.1.md §3.5 (`readyWhen` vs `propagateWhen`), §3.6 (dependency edge mechanism), spec 01 (Graph CRD schema), and spec 02 (node templates).

Key semantic facts as of 2026-04-10 (verify against krocodile before implementing):
- `readyWhen` = health signal (UI, `kubectl get graph`) — does NOT block downstream
- `propagateWhen` = data-flow gate — DOES block downstream when unsatisfied
- `spec.nodes` (not `spec.resources`) is the field name for the node list
- `experimental.kro.run` is the API group for Graph CRDs (changed from `kro.run` in krocodile commit `48224264`)
- Bug fix (2026-04-10 commit `94a24fa5`): `dep.ready()` in `readyWhen` was not re-evaluating after dep became ready — now fixed. Our `propagateWhen` usage is unaffected.
- Bug fix (2026-04-10 commit `1b0ce353`): double-dispatch race in DAG coordinator — now fixed in krocodile. **Pinned krocodile commit: `1b0ce353` (minimum required).**

## Release and Versioning Philosophy

**GA (`v1.0.0`) is not a near-term goal.** Do not plan for or reference GA. We are a
pre-release project — the focus is on shipping working, usable minor versions iteratively.

### Versioning approach

- **Minor versions (`v0.N.0`)** are substantial. Each minor ships meaningful, usable
  new capability and takes multiple stages to complete. There is no upper bound on minor
  versions — we may ship `v0.10.0`, `v0.20.0`, etc. as the project evolves. Minor versions
  are cut when the milestone's open issues reach zero and its associated journeys pass.

- **Patch versions (`v0.N.P`)** are bug fixes, security patches, and doc corrections.
  Cut them whenever needed — they do not require a full milestone to complete.

- **No `v1.0.0` planning.** Do not create a v1.0.0 milestone. When we are genuinely
  production-ready we will decide on GA deliberately, not as a roadmap item.

### Milestone scope per minor

Milestones should be **small enough to ship in weeks, not months**. A minor milestone
covers 2-4 stages maximum. If a group of stages would take more than ~6 weeks, split
it into two milestones. Each milestone must have a clear "what can a user do after this"
answer — if you can't state it in one sentence, the scope is too large.

### PM instructions for milestone creation

- Derive milestones from `docs/aide/roadmap.md` stage groupings
- Each milestone title is `v0.N.0` (no v1.0.0)
- Each milestone description states: stages covered, what users can do, which journeys unlock
- Future milestones beyond the next two get epic issues only (no full specs)
- When cutting a release: use `gh release create`, generate notes from closed issues,
  close the milestone, open the next one, post `[📋 PM] RELEASE: vX.Y.Z` on the report issue

## Goals and Objectives

1. Provide a declarative, Kubernetes-native promotion system where every object is a CRD and every state transition is observable via kubectl.
2. Make every promotion a Git pull request with rich evidence: artifact provenance, upstream verification, and policy gate compliance.
3. Represent policy gates as visible nodes in the promotion DAG, inspectable via CLI (`kardinal explain`) and UI, with org-level gates that teams cannot bypass.
4. Support DAG-structured pipelines (parallel fan-out, conditional steps, multi-service dependencies) via kro's Graph primitive.
5. Work with existing GitOps tools (Argo CD, Flux) via pluggable health adapters that auto-detect installed CRDs. Also work without a GitOps tool.
6. Support multi-cluster promotion via Argo CD hub-spoke, Flux with remote kubeconfig, or bare Kubernetes.
7. Provide pluggable promotion steps so teams can inject custom logic (tests, approvals, notifications) between built-in Git and health operations.
8. Support both image promotions (new container versions) and config-only promotions (resource limits, env vars, feature flags) through the same pipeline.
9. Enable distributed deployments where agents run behind firewalls and connect outbound to a control plane.

## Target Users

### Platform engineers (primary)

Define promotion pipelines and PolicyGates for their organization. Configure health adapters, Git providers, and delivery delegation. Manage org-level policies that application teams inherit.

### Application developers (secondary)

Create Bundles (via CI webhook or CLI) to trigger promotions. Review and merge promotion PRs. Use `kardinal explain` to understand why a promotion is blocked. Use the UI to monitor promotion progress.

### SREs and operators (secondary)

Use `kardinal rollback` and `kardinal pause` during incidents. Monitor promotion health via the UI and controller metrics. Configure shard assignments for distributed deployments.

## Core Features

### F1: Pipeline CRD

A user-facing CRD that defines the promotion path for one application. Lists environments in order with Git configuration. The controller translates this into a kro Graph, injecting PolicyGate nodes.

Environments promote sequentially by default. For parallel fan-out, use the `dependsOn` field. Each environment specifies approval mode (`auto` or `pr-review`), update strategy (`kustomize` or `helm`), health adapter, and optional delivery delegation.

### F2: Bundle CRD

An immutable, versioned snapshot of what to deploy. Types: `image` (container images), `config` (Git commit with configuration changes), `mixed` (both). Carries build provenance (commit SHA, CI run URL, author, digest).

Created by CI webhook, CLI, kubectl apply, or Subscription CRD. Includes intent (target environment, skip list with permission enforcement).

Phases: Available, Promoting, Verified, Failed, Superseded. Per-environment evidence (metrics, gate results, approver, timing) is stored in Bundle status for durable audit.

### F3: PolicyGate CRD

CEL-powered policy checks represented as nodes in the promotion Graph. Platform teams define org-level gates (in `platform-policies` namespace) that are automatically injected into every Pipeline targeting matching environments. Teams can add their own gates but cannot remove org gates.

CEL context includes: bundle metadata, schedule (isWeekend, hour, dayOfWeek), environment info, metrics (Phase 2), upstream soak time (Phase 2).

Re-evaluation via `recheckInterval` for time-based gates. `lastEvaluatedAt` freshness prevents stale gate state.

SkipPermission gates control whether `intent.skip` is allowed on gated environments.

### F4: Promotion Steps Engine

Configurable step sequence per environment. Default sequence inferred from update strategy and approval mode. Custom steps via HTTP webhook.

Built-in steps: `git-clone`, `kustomize-set-image`, `helm-set-image`, `kustomize-build`, `config-merge`, `git-commit`, `git-push`, `open-pr`, `wait-for-merge`, `health-check`.

Custom steps: any `uses` value not matching a built-in step dispatches as HTTP POST to the configured URL. Returns pass/fail. Steps pass outputs to subsequent steps via an accumulator.

### F5: Health Adapters

Pluggable, auto-detected health verification. Phase 1 adapters: Deployment condition (`resource`), Argo CD Application health+sync (`argocd`), Flux Kustomization Ready (`flux`). Phase 2: Argo Rollouts (`argoRollouts`), Flagger (`flagger`).

Auto-detection: controller checks for CRDs on startup and periodically. Priority: argocd, flux, resource. Remote cluster support via kubeconfig Secrets.

### F6: PR Evidence

For `pr-review` environments, the controller opens a PR with structured body: policy gate compliance table, artifact provenance with links, upstream verification timestamps, commit range diff. Labels for filtering (`kardinal`, `kardinal/promotion`, `kardinal/rollback`, `kardinal/emergency`). Merge detection via webhook with startup reconciliation fallback.

### F7: Distributed Architecture

Control plane (kardinal-controller) handles Pipeline reconciliation, Graph generation, PolicyGate evaluation. Agents (kardinal-agent) handle PromotionStep reconciliation per shard. Agents run behind firewalls, connect outbound to control plane. Shard routing via `kardinal.io/shard` label. Credential isolation: Git tokens and health credentials stay in agent clusters.

Standalone mode (Phase 1): single binary handles everything. Distributed mode (Phase 2+): controller + agents. Same reconciler code in both modes.

### F8: kardinal-ui

Embedded React UI served by the controller binary via `go:embed`. Read-only. Renders the promotion DAG with per-node state (PromotionStep: green/amber/red; PolicyGate: pass/fail/pending). Shows Bundle provenance, PR links, policy evaluation details. Backend API proxy at `/api/v1/ui/` reads CRDs from the Kubernetes API server.

### F9: CLI

Single static Go binary. Commands: `init`, `get pipelines/steps/bundles`, `create bundle`, `promote`, `explain` (with `--watch`), `rollback` (with `--emergency`), `pause`, `resume`, `history`, `policy list/test/simulate`, `diff`, `version`. All commands create or read CRDs.

### F10: Config-Only Promotions

Bundle `type: config` references a Git commit SHA. The `config-merge` step applies changes via cherry-pick or overlay. Config Bundles go through the same Pipeline, PolicyGates, and PR flow. Config and image Bundles coexist independently (different types do not supersede each other). Phase 3: mixed Bundles (image + config), Git Subscription for passive config watching.

### F11: Subscription CRD (Phase 3)

Declarative registry and Git watcher. Image subscriptions watch OCI registries for new tags. Git subscriptions watch repositories for config changes. Auto-creates Bundles when new artifacts are discovered.

## Technical Architecture

### Stack

- **Language:** Go 1.23+
- **Kubernetes:** controller-runtime, dynamic client, CRDs via kubebuilder
- **DAG engine:** kro Graph primitive (`kro.run/v1alpha1/Graph`)
- **Policy expressions:** CEL via `google/cel-go`
- **Git:** go-git or shell exec for clone/push, SCM provider interface for PRs
- **UI:** React 19, TypeScript, Vite, embedded via `go:embed`
- **CLI:** cobra

### CRDs

| CRD | User-created? | Purpose |
|---|---|---|
| Pipeline | Yes | Promotion topology, Git config, environments |
| Bundle | Yes (via CI/CLI/kubectl) | Artifact snapshot with provenance and intent |
| PolicyGate | Yes (platform/team) | CEL policy check, DAG node template |
| PromotionStep | No (created by Graph) | Per-environment promotion execution state |
| MetricCheck | Yes (optional, Phase 2) | Prometheus/Datadog query template |
| Subscription | Yes (optional, Phase 3) | Registry/Git watcher |
| Graph | No (created by controller) | kro DAG spec (auto-generated from Pipeline) |

### Pluggable Interfaces

| Interface | Purpose | Phase 1 impl | Future impls |
|---|---|---|---|
| `scm.SCMProvider` | PR lifecycle (create, merge, comment) | GitHub | GitLab, Bitbucket |
| `scm.GitClient` | Git operations (clone, push) | go-git/shell | Same across providers |
| `update.Strategy` | Manifest update | Kustomize | Helm, replace, OCI |
| `health.Adapter` | Health verification | Deployment, Argo CD, Flux | Argo Rollouts, Flagger |
| `delivery.Delegate` | Progressive delivery watch | none (instant) | Argo Rollouts, Flagger |
| `metrics.Provider` | Metric queries | (Phase 2) | Prometheus, Datadog |
| `source.Watcher` | Artifact discovery | (Phase 3) | OCI registry, Git |
| `steps.Step` | Promotion step | 10 built-in | Custom webhooks |

### Multi-Cluster Model

- Argo CD hub-spoke: controller reads Application health from hub cluster, no cross-cluster API calls
- Flux per-cluster: remote kubeconfig Secret for health verification
- Bare Kubernetes: remote kubeconfig Secret
- Parallel fan-out: `dependsOn` on Pipeline environments creates parallel Graph nodes

### Graph Integration

The controller generates a Graph spec per Bundle. Dependency edges are inferred from CEL `${}` references between node templates. Each PromotionStep and PolicyGate carries fields (`upstreamVerified`, `upstreamEnvironment`, `requiredGates`) that serve both reconciler logic and edge creation.

Per-Bundle Graph lifecycle: created on Bundle promotion start, owned by Bundle via ownerReferences, cascade-deleted on Bundle GC.

## Non-Functional Requirements

### Performance

- Controller startup in under 10 seconds
- Pipeline-to-Graph translation in under 1 second
- PolicyGate CEL evaluation in under 10ms
- PR creation in under 5 seconds (GitHub API dependent)
- Health check polling every 10 seconds during HealthChecking state
- Git clone from cache in under 2 seconds (shallow clone, shared work trees)

### Scalability

- Designed for fewer than 50 Pipelines per control plane in Phase 1
- Distributed mode (Phase 2) scales horizontally via sharded agents
- Git rate limits: no polling, webhook-only merge detection with startup reconciliation
- PolicyGate recheck: ~20 writes/minute at 50 pipelines with `recheckInterval: 5m`

### Security

- Pod security: runAsNonRoot, readOnlyRootFilesystem, drop ALL capabilities
- RBAC: minimum required verbs per CRD, org PolicyGates protected by namespace RBAC
- Webhook auth: X-Hub-Signature-256 (SCM), Bearer+HMAC (Bundles), rate limited
- Credential isolation: in distributed mode, Git tokens stay in agent clusters
- UI: read-only, separate port available via `--ui-listen-address`

### Reliability

- All reconcilers are idempotent (safe to re-run after crash)
- Leader election via Kubernetes Lease (15s failover)
- Graph controller down: existing steps continue, new steps paused
- Agent down: sharded steps paused, resume on restart
- PR merge detection: webhook primary, startup reconciliation fallback
- PolicyGate staleness: `lastEvaluatedAt` freshness check prevents stale advancement

## Constraints and Assumptions

### Dependencies

- kro's Graph controller must be available in the cluster (experimental, API may change)
- A GitOps tool (Argo CD, Flux, or equivalent) must be syncing from the Git repository
- CI must create Bundles (via webhook, CLI, or kubectl) in Phase 1. Subscription CRD (Phase 3) removes CI requirement.

### Assumptions

- Teams have an existing GitOps repository with Kustomize or Helm per-environment directories
- Environment-specific configuration (secrets, resource limits) is already managed in the GitOps repo
- GitHub is the Git provider in Phase 1. GitLab in Phase 2.

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

## Success Criteria

### Phase 1a (weeks 1-6)

- A user can apply a Pipeline CRD and a Bundle, and see the promotion flow through 3 environments (test, uat, prod) with correct ordering.
- PolicyGates block production promotion on weekends and enforce upstream soak time.
- PRs contain promotion evidence (provenance, upstream verification, policy compliance).
- `kardinal explain` shows which gates are blocking and why.
- E2E test passes: kind cluster + Graph controller + GitHub repo + 3-env pipeline + PolicyGate blocking.

### Phase 1b (weeks 7-12)

- kardinal-ui renders the promotion DAG with per-node state.
- Health verification works with Argo CD, Flux, and bare Kubernetes (auto-detected).
- Multi-cluster health via remote kubeconfig Secrets.
- `kardinal init` generates a Pipeline from 8 lines of config.
- GitHub Action creates Bundles from CI.

### Phase 2 (weeks 13-20)

- Argo Rollouts and Flagger delegation.
- Distributed mode with kardinal-agent.
- Custom promotion steps via webhook.
- Config-only Bundles with config-merge step.
- Rollback CLI and automatic rollback on failure.
- MetricCheck CRD with Prometheus.
- `kardinal policy simulate`.
- GitLab support.

### Phase 3 (weeks 21-28)

- Subscription CRD for registry and Git watching.
- Direct Graph authoring for power users.
- Mixed Bundles (image + config).
- Webhook gates.
- Security hardening.

## Competitive Landscape

| Dimension | kardinal-promoter | Kargo (v1.9.5) | GitOps Promoter (v0.26.3) |
|---|---|---|---|
| Pipeline model | Graph DAG (fan-out, conditional, forEach) | Stage dependencies (configurable) | Linear branch promotion |
| Policy governance | PolicyGate Graph nodes (CEL), visible in UI | None shipped; planned (#3440) | Commit statuses |
| GitOps integration | Auto-detects Argo CD, Flux, bare K8s | Argo CD for health verification | Any (commit statuses) |
| PR approval | Evidence (provenance, metrics, policy table) | git-open-pr / wait-for-pr (since v1.8.0) | PR with branch diff |
| Artifact bundling | Bundle CRD (images, Helm, Git ref, provenance) | Freight (images, charts, Git refs, digests) | None (raw Git diff) |
| Rollback | Forward promotion of prior Bundle | Re-promote prior Freight; AnalysisTemplate | Revert PR manually |
| Multi-cluster | Argo CD hub-spoke, Flux via kubeconfig | Argo CD hub-spoke | Branch structure |
| Maturity | Pre-release | v1.9.x (stable) | v0.26.x (experimental) |

### Key differentiators

1. PolicyGates as visible DAG nodes (no competitor has this)
2. GitOps-tool agnostic with auto-detection (Kargo requires Argo CD)
3. Build provenance on artifacts (Kargo Freight has no CI run link)
4. Pluggable promotion steps with custom webhooks
5. Fully declarative (no API server, all state in K8s CRDs)
6. Config-only promotions through the same pipeline

## Workshop Benchmarks

The design has been validated against two AWS workshops:

1. [Platform Engineering on EKS: Production Deploy with Kargo](https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo)
2. [Fleet Management on Amazon EKS: Promote App to Prod Clusters](https://github.com/aws-samples/fleet-management-on-amazon-eks-workshop/tree/mainline/patterns/kro-eks-cluster-mgmt)

Both scenarios (multi-cluster promotion with parallel prod fan-out and Argo Rollouts canary) are achievable with the Pipeline CRD, PolicyGate, and health adapter architecture. Working examples are in `examples/quickstart/` and `examples/multi-cluster-fleet/`.

## Reference Documents

| Document | Location |
|---|---|
| Technical design | `docs/design/design-v2.1.md` |
| Implementation specs | `docs/design/01-graph-integration.md` through `09-config-only-promotions.md` |
| User quickstart | `docs/quickstart.md` |
| Core concepts | `docs/concepts.md` |
| CLI reference | `docs/cli-reference.md` |
| Pipeline reference | `docs/pipeline-reference.md` |
| Policy gates guide | `docs/policy-gates.md` |
| Health adapters guide | `docs/health-adapters.md` |
| Rollback guide | `docs/rollback.md` |
| CI integration guide | `docs/ci-integration.md` |
| PR evidence guide | `docs/pr-evidence.md` |
| Troubleshooting | `docs/troubleshooting.md` |
| Quickstart example | `examples/quickstart/` |
| Multi-cluster example | `examples/multi-cluster-fleet/` |
| Constitution | `.specify/memory/constitution.md` |
