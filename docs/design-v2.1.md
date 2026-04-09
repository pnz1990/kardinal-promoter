# kardinal-promoter: Technical Design Document

> Version: 2.1
> Date: 2026-04-09
> License: Apache 2.0

## Overview

kardinal-promoter is a Kubernetes-native promotion controller. It moves versioned artifact bundles through environment pipelines using Git pull requests as the approval mechanism, with policy gates expressed as CEL and represented as visible nodes in the promotion DAG.

The execution engine is [kro's Graph primitive](https://github.com/ellistarn/kro/tree/krocodile/experimental), a general-purpose Kubernetes DAG reconciler. Graph handles dependency ordering, parallel execution, conditional inclusion, and teardown. kardinal-promoter handles the promotion-specific logic: Git writes, PR lifecycle, policy evaluation, and health verification.

All state lives in Kubernetes CRDs. There is no external database, no dedicated API server, and no state outside of etcd. The CLI, UI, and webhook endpoints create and read CRDs. A user can operate the entire system with `kubectl`.

---

## Relationship to kro and Graph

kro's Graph primitive (`kro.run/v1alpha1/Graph`) is a namespace-scoped CRD that defines a set of nodes with Kubernetes resource templates, CEL expressions for data flow, and dependency inference from those expressions. It supports `readyWhen`, `includeWhen`, `forEach`, `propagateWhen`, and `finalizes` on each node. The Graph controller reconciles the DAG using scoped walks and hash-based change detection. Reference implementation: [ellistarn/kro/tree/krocodile/experimental](https://github.com/ellistarn/kro/tree/krocodile/experimental).

Within the kro ecosystem, the relationship is:

- **Graph** is the core DAG primitive. It creates, reconciles, and tears down sets of Kubernetes resources in dependency order.
- **RGD** (ResourceGraphDefinition) is a higher-level concept built on Graph for resource composition and CRD generation.
- **kardinal-promoter** is a separate concept built on Graph for promotion orchestration and policy gating.

The advantage of building on Graph directly (rather than on RGD) is that there is no translation shim. With RGD, the controller would need to generate an RGD spec from the Pipeline CRD, which introduces an abstraction layer between the user's intent and the execution engine. The RGD layer also carries resource-composition semantics (CRD generation, schema validation, instance controllers) that are irrelevant to promotion. With Graph, the controller generates a Graph spec whose nodes are exactly the PromotionStep and PolicyGate CRDs that kardinal-promoter needs. The Graph controller creates those CRDs in DAG order. No shim, no intermediate abstraction, no unused semantics.

---

## 1. Design Principles

1. **The PR is the approval surface.** Every promotion (forward or rollback) produces a Git pull request. The PR body contains promotion evidence: artifact provenance, upstream verification results, and policy gate compliance. Human approval for gated environments happens by merging the PR.

2. **Artifacts, not diffs.** Promotions track versioned, immutable Bundles with build provenance (commit SHA, CI run URL, author, image digest), not opaque Git diffs.

3. **Works with existing GitOps tools.** No dependency on Argo CD or Flux. Integrates with both via pluggable health adapters that auto-detect installed CRDs. Also works without a GitOps tool (falls back to Deployment condition checks).

4. **Rollback is a forward promotion.** Rolling back to a previous version creates a new Bundle targeting the rollback version and runs it through the same pipeline, same policy gates, same PR flow.

5. **Pipelines are graphs.** Every pipeline, including linear ones, runs as a kro Graph internally. Parallel fan-out, conditional steps, and multi-service dependencies are native to Graph. Power users can write Graph specs directly.

6. **Policies are DAG nodes.** PolicyGates are nodes in the Graph between environment steps. They are visible in the UI, inspectable via CLI, and block downstream steps until their CEL expression evaluates to true. Org-level gates are injected as mandatory dependencies that team-level Pipelines cannot remove.

7. **Kubernetes is the control plane.** Every object is a CRD. etcd is the database. The API server is the API. CLI, UI, and webhook endpoints are convenience layers that create and read CRDs.

8. **Never mutate workload resources.** The controller never creates, updates, or deletes Deployments, Services, or HTTPRoutes. All cluster state changes flow through Git. In-cluster progressive delivery is delegated to Argo Rollouts or Flagger.

9. **Pluggable by default.** Every integration point (SCM providers, manifest update strategies, health verification, delivery delegation, metric providers, artifact sources) is a Go interface. Phase 1 ships one implementation per interface. Adding a provider means implementing the interface and registering it.

---

## 2. Concepts

### 2.1 Bundle

An immutable, versioned snapshot of what to deploy. Contains one or more container image references (tag + digest), optionally a Helm chart version or Git commit SHA. Carries build provenance.

Creation paths (all produce the same Bundle CRD in etcd):
- CI webhook: `POST /api/v1/bundles`
- CLI: `kardinal create bundle my-app --image ghcr.io/myorg/my-app:1.29.0`
- kubectl: `kubectl apply -f bundle.yaml`
- Subscription CRD: auto-created from registry (Phase 3)

Phases: `Available`, `Promoting`, `Verified`, `Failed`, `Superseded`.

The `spec.intent.target` field declares how far to promote (default: all environments). The `spec.intent.skip` field excludes environments from the promotion, subject to SkipPermission validation (Section 5.6).

### 2.2 Pipeline

A CRD listing environments with Git configuration. The controller translates this into a Graph, injecting PolicyGate nodes based on org and team policies. Users do not need to know Graph exists unless they want non-linear topologies (parallel fan-out, conditional steps), in which case they write Graphs directly.

### 2.3 PromotionStep

A CRD representing one environment promotion. Created by the Graph controller as a node in the promotion Graph. The kardinal-controller watches PromotionStep CRs and executes: Git write, PR creation, merge detection, health verification, status update.

The Graph's `readyWhen` on the PromotionStep node is `${dev.status.state == "Verified"}`. When the step reaches Verified, Graph advances to dependent nodes.

### 2.4 PolicyGate

A CRD representing a policy check. PolicyGates are nodes in the Graph between environment steps. They block downstream promotion until their CEL expression evaluates to true.

```
[staging Verified] --> [no-weekend-deploys PASS] --> [prod waiting-for-PR]
                       [staging-soak PASS]       /
```

Graph creates PolicyGate CRs. The kardinal-controller evaluates the CEL expression, writes `status.ready` and `status.lastEvaluatedAt`, and Graph reads the result via `readyWhen`.

Org-level PolicyGates (from the `platform-policies` namespace) are injected as mandatory dependencies during Pipeline-to-Graph translation. Teams cannot remove them. Teams can add their own gates from their own namespace, which are wired alongside org gates.

### 2.5 Subscription (Phase 3)

A declarative CRD that watches an OCI registry and auto-creates Bundles when new tags matching a semver constraint are discovered.

---

## 3. Architecture

### 3.1 Component Overview

```
UX Surfaces (convenience, not required):
  CLI:     kardinal promote  -->  patches Bundle CR
  UI:      kardinal-ui       -->  reads CRDs, renders DAG
  Webhook: /api/v1/bundles   -->  creates Bundle CR
  CI:      GitHub Action     -->  creates Bundle CR

Kubernetes API Server (sole source of truth):
  kardinal CRDs:  Pipeline, Bundle, PromotionStep, PolicyGate,
                  MetricCheck (Phase 2), Subscription (Phase 3)
  Graph CRDs:     Graph, GraphRevision

Controllers:
  Graph controller:
    Reconciles Graph CRs
    Creates child resources (PromotionStep, PolicyGate CRs) in DAG order
    Handles: dependency ordering, parallel execution, includeWhen,
             forEach, readyWhen, propagateWhen, scoped walks,
             change detection, teardown

  kardinal-controller:
    Pipeline reconciler:
      Watches Pipeline + Bundle CRDs
      Generates per-Bundle Graph spec with PolicyGate injection
      Validates intent.skip against SkipPermission gates
    PromotionStep reconciler:
      Git write, PR creation, merge detection, health check, status update
    PolicyGate reconciler:
      CEL evaluation, status.ready + status.lastEvaluatedAt
      Timer-based re-evaluation at recheckInterval (see Section 3.5)

  kardinal-ui:
    Embedded in controller binary (React via go:embed)
    Served at /ui (separate port via --ui-listen-address)
    Read-only: reads Graph, PromotionStep, PolicyGate, Bundle CRDs
```

### 3.2 Why Graph Instead of RGD

RGD is a resource-composition abstraction. It generates CRDs from schema definitions, watches instances of those CRDs, and manages child resources per instance. This machinery is useful for its intended purpose but is unnecessary overhead for promotion orchestration.

Building on RGD requires a shim layer: the controller generates an RGD spec from the Pipeline CRD, then the RGD controller creates a CRD, then an instance of that CRD is created, which triggers the RGD instance controller to create the actual resources. That is three levels of indirection.

Building on Graph is direct: the controller generates a Graph spec whose nodes are PromotionStep and PolicyGate CRDs. The Graph controller creates those CRDs in dependency order. Two levels: Pipeline to Graph spec, Graph spec to resources. The generated Graph looks exactly like what runs. There is no intermediate CRD generation or instance indirection.

| Aspect | With RGD | With Graph |
|---|---|---|
| Indirection levels | Pipeline to RGD to CRD to instance to resources | Pipeline to Graph to resources |
| Unused features | CRD generation, schema validation, instance controllers | None |
| Dependency ordering | Available (same CEL-based inference) | Available (same CEL-based inference) |
| readyWhen / includeWhen / forEach | Available | Available |
| Scoped walks + change detection | Available (in kro) | Available (native to Graph) |
| Status aggregation | RGD Accepted/Ready | Graph Accepted/Ready |

The feature set is equivalent. The difference is that Graph does not carry resource-composition abstractions that promotion does not use.

### 3.3 Promotion Flow

1. Bundle CR created (webhook, CLI, kubectl, or Subscription).
2. kardinal-controller detects new Bundle. Validates `intent.skip` if present (see Section 5.6). Generates a Graph spec from the Pipeline CRD, tailored to the Bundle's intent. Injects org/team PolicyGate nodes. Creates a Graph CR owned by the Bundle via `ownerReferences`.
3. Graph controller reconciles the Graph. Resolves DAG dependencies. Creates the first PromotionStep CR (e.g., dev).
4. kardinal-controller sees the PromotionStep. Clones the Git repo (from cached work tree), updates manifests using the configured update strategy, pushes. For `approval: auto`, pushes directly to the target branch. For `approval: pr-review`, opens a PR with promotion evidence.
5. For PR-gated environments: waits for merge (webhook primary, 5m background sweep fallback).
6. After merge: monitors health via the appropriate adapter (Deployment, Argo CD, Flux).
7. When healthy: writes `status.state = "Verified"`. Copies promotion evidence (metrics, gate results, approver, timing) into Bundle `status.environments` for durable audit storage.
8. Graph controller sees `readyWhen` satisfied. Creates next nodes (PolicyGates, then PromotionStep for the next environment).
9. kardinal-controller evaluates PolicyGates: CEL expression against promotion context. Writes `status.ready` and `status.lastEvaluatedAt`.
10. Graph controller sees all PolicyGates ready. Creates next PromotionStep.
11. Repeat until all environments are Verified or a step fails.
12. On failure: `status.state = "Failed"`. Graph stops all downstream nodes. kardinal-controller opens rollback PRs for affected environments.

### 3.4 Per-Bundle Graph Lifecycle

Each Bundle gets its own Graph CR. The Graph spec is generated from the Pipeline CRD and tailored to the Bundle's intent (`target`, `skip`). The Graph is owned by the Bundle via `ownerReferences`.

| Event | Behavior |
|---|---|
| Bundle created | Controller validates intent, generates Graph, creates Graph CR |
| Promotion completes | Graph remains (audit record). Bundle phase set to Verified. |
| Promotion fails | Graph remains (failed nodes visible in UI). Bundle phase set to Failed. |
| Superseded by newer Bundle | Old Graph deleted via Bundle ownerRef cascade. Old Bundle set to Superseded. Pinned Bundles (`kardinal.io/pin: "true"`) are not superseded. |
| History limit exceeded | Oldest Bundles and their Graphs are garbage-collected. |

Evidence (metrics, gate results, approver, timing) is copied into Bundle `status.environments` at verification time (Step 7 above). When the Graph and its PromotionStep CRs are garbage-collected, the evidence survives on the Bundle. When the Bundle itself is garbage-collected (history limit), the Git PRs remain as the permanent audit trail.

### 3.5 PolicyGate Re-evaluation (recheckAfter Workaround)

Graph reconciles on Kubernetes watch events, not on timers. A `no-weekend-deploys` PolicyGate that evaluates `!schedule.isWeekend` will set `status.ready = false` on Friday evening. On Monday morning, no cluster state has changed, so no watch event fires and Graph does not re-evaluate the gate.

The Phase 1 workaround: the kardinal-controller's PolicyGate reconciler runs a timer-based re-evaluation loop. For each in-flight PolicyGate instance, it re-evaluates the CEL expression at the configured `recheckInterval` and writes `status.lastEvaluatedAt`. This status write triggers a watch event, which causes Graph to re-check the `readyWhen` expression.

This is not free. At 50 pipelines with 2 prod gates each, the reconciler writes 100 status updates every 5 minutes (at `recheckInterval: 5m`). This is acceptable for the expected scale (<50 pipelines). At larger scale, Graph should support a native `recheckAfter` hint on nodes (see Section 17).

The `readyWhen` on each PolicyGate node includes a freshness check:

```yaml
readyWhen:
  - ${noWeekendDeploys.status.ready == true}
  - ${timestamp(noWeekendDeploys.status.lastEvaluatedAt) > now() - duration("10m")}
```

If the kardinal-controller restarts and has not yet re-evaluated a gate, `lastEvaluatedAt` will be stale. Graph treats the gate as not-ready until the controller catches up. This prevents promotions from advancing on stale gate state.

### 3.6 Dependency Edges in Generated Graphs

Graph infers dependency edges from CEL `${}` references between nodes. If node B's template contains `${A.status.state}`, B depends on A.

The current Graph implementation does not support an explicit `dependsOn` field on nodes. To express ordering between PromotionSteps and PolicyGates, the controller uses two mechanisms:

**For PromotionSteps:** Each step's template includes a `spec.upstreamVerified` field that references the upstream step's status: `${dev.status.state}`. This field is consumed by the PromotionStep reconciler (it checks that the upstream is Verified before proceeding), so it serves both a semantic purpose and creates the dependency edge.

**For PolicyGates:** Each gate's template includes a `spec.upstreamEnvironment` field referencing the upstream PromotionStep: `${staging.status.state}`. This tells the PolicyGate reconciler which environment's verification to check for soak time calculations, and it creates the dependency edge.

**For downstream PromotionSteps after PolicyGates:** The prod PromotionStep's template includes `spec.requiredGates` referencing the gate statuses: `["${noWeekendDeploys.metadata.name}", "${stagingSoak.metadata.name}"]`. This creates fan-in edges from multiple gates to the step.

These fields are not synthetic placeholders. They carry information the reconcilers use. The dependency edge inference is a secondary effect of data that is already needed.

A native `dependsOn` on Graph nodes would be cleaner. This is tracked as a proposed contribution (Section 17).

### 3.7 UX Surface Roles

The system has three surfaces that show promotion state. Each serves a different purpose:

| Surface | Role | What it shows | When to use |
|---|---|---|---|
| GitHub PR | Approval surface | Snapshot at PR creation + periodic comment updates. Artifact diff, provenance, policy gate table, upstream verification. | Reviewing and approving a promotion. |
| kardinal-ui | Monitoring surface | Live Graph state from etcd. DAG visualization with per-node status. Bundle list, PR links, policy evaluation details. | Watching promotion progress across all environments. |
| kubectl / CLI | Debug surface | Raw CRD status. `kardinal explain` shows PolicyGate evaluation trace. | Diagnosing why a promotion is stuck. |

The PR comment is a snapshot. It is updated when gate states change (via webhook), but between updates it can be stale relative to the live CRD state. The UI and kubectl always show live state. Authorization for PR approval is governed by Git CODEOWNERS. Authorization for CRD operations is governed by Kubernetes RBAC. These are independent; the controller does not attempt to reconcile them.

### 3.8 GitOps Tool Integration

Health verification adapters (pluggable, auto-detected):

| Adapter | What it watches | Healthy when | Phase |
|---|---|---|---|
| `resource` (default) | `Deployment.status.conditions` | `Available=True`, not stalled | Phase 1 |
| `argocd` | `Application.status.health` + `.sync` | `Healthy` AND `Synced` | Phase 1 |
| `flux` | `Kustomization.status.conditions` | `Ready=True`, generation match | Phase 1 |
| `argoRollouts` | `Rollout.status.phase` | `Healthy` | Phase 2 |
| `flagger` | `Canary.status.phase` | `Succeeded` | Phase 2 |

On startup, the controller calls `Available()` on each registered adapter to check for installed CRDs. Per-environment health type can be set explicitly or auto-detected (priority: argocd, flux, resource).

Kargo's promotion steps operate on Git directly and can technically work without Argo CD for the promotion mechanics (git-clone, git-push, kustomize-set-image are Argo CD-independent). However, Kargo's health verification and Freight tracking are deeply integrated with Argo CD Application status. kardinal-promoter's health adapters provide equivalent depth of integration for both Argo CD and Flux users without requiring either.

### 3.9 Multi-Cluster Promotion

Promotion is a Git write. Git has no cluster boundary. Each cluster has its own GitOps tool syncing its own directory from the shared Git repository.

For health verification across clusters:

- **Argo CD hub-spoke model:** Argo CD Applications for all clusters live in the hub. The controller reads Application health from the hub cluster. No cross-cluster API calls needed.
- **Flux per-cluster model:** Each cluster runs its own Flux. Health verification uses a `cluster` field on the environment config that references a kubeconfig Secret for the remote cluster.
- **Bare Kubernetes:** Same as Flux. Remote kubeconfig Secret.

Parallel fan-out to multiple regions is expressed via `dependsOn` on the Pipeline CRD:

```yaml
environments:
  - name: staging
  - name: prod-us-east
    dependsOn: [staging]
    approval: pr-review
  - name: prod-eu-west
    dependsOn: [staging]
    approval: pr-review
```

Both prod environments depend on staging. The generated Graph creates them as parallel nodes. Graph executes them concurrently.

### 3.10 Bundle Creation

Four paths, one result. All create a Bundle CRD in etcd.

| Path | Mechanism | When to use |
|---|---|---|
| CI webhook | `POST /api/v1/bundles` with JSON body | CI pipelines (GitHub Actions, GitLab CI) |
| CLI | `kardinal create bundle my-app --image ...` | Manual or scripted |
| kubectl | `kubectl apply -f bundle.yaml` | Fully declarative GitOps |
| Subscription | Controller auto-creates from registry | Passive trigger, no CI changes (Phase 3) |

### 3.11 kardinal-ui

Embedded in the controller binary. React frontend bundled via `go:embed`. Served at `/ui`. Read-only; all mutations go through CRDs.

For environments with network segmentation requirements, `--ui-listen-address` separates the UI onto a different port from the API endpoints (`/api/v1/bundles`, `/webhooks`, `/metrics`).

Features: promotion DAG with per-node state (PromotionStep: green/amber/red; PolicyGate: pass/fail/pending), Bundle list with provenance, PR links, policy evaluation details, pipeline overview.

### 3.12 Git Caching

Shared work trees per repo URL at `/var/cache/kardinal/<repo-hash>/`. Initial access: shallow clone (`--depth 1`). Subsequent: `git fetch --depth 1 && git reset --hard`. Mutex-serialized per work tree. Cache invalidated on repo URL change, 24h age, or Git operation failure.

### 3.13 Controller Metrics

The controller exposes a `/metrics` endpoint in Prometheus format.

| Metric | Type | Description |
|---|---|---|
| `kardinal_bundles_created_total` | Counter | Bundles created (by pipeline, source) |
| `kardinal_promotions_opened_total` | Counter | PRs opened (by env, approval type) |
| `kardinal_promotions_merged_total` | Counter | PRs merged (by env) |
| `kardinal_promotions_failed_total` | Counter | Promotions failed (by env, reason) |
| `kardinal_rollbacks_total` | Counter | Rollbacks triggered (by env, trigger type) |
| `kardinal_policy_gates_evaluated_total` | Counter | PolicyGate evaluations (by result) |
| `kardinal_policy_gates_blocked_total` | Counter | Promotions blocked by gate (by gate name) |
| `kardinal_health_check_duration_seconds` | Histogram | Health check latency |
| `kardinal_git_operation_duration_seconds` | Histogram | Git operation latency |
| `kardinal_scm_api_requests_total` | Counter | SCM API calls (by endpoint, status) |
| `kardinal_promotion_lead_time_seconds` | Histogram | Time from Bundle creation to environment verification |

---

## 4. Pluggable Architecture

Every integration point is a Go interface. Phase 1 ships one implementation per interface. Adding a provider means implementing the interface and registering it in the provider registry.

### 4.1 Extension Points

**SCM Provider** (`pkg/scm/provider.go`): Handles Git operations and PR lifecycle.

```go
type GitClient interface {
    Clone(ctx context.Context, url string, opts CloneOptions) (*Repository, error)
    Push(ctx context.Context, repo *Repository, branch string) error
}

type SCMProvider interface {
    CreatePR(ctx context.Context, opts PROptions) (*PullRequest, error)
    GetPRStatus(ctx context.Context, repo string, prNumber int) (PRStatus, error)
    MergePR(ctx context.Context, repo string, prNumber int) error
    UpdatePRComment(ctx context.Context, repo string, prNumber int, body string) error
    ValidateWebhook(ctx context.Context, headers http.Header, body []byte, secret string) (bool, error)
}
```

Git operations are provider-agnostic (clone/push work the same for all providers). PR operations are provider-specific. Splitting the interface allows reusing one Git implementation across providers.

Behavioral contracts: `CreatePR` must be idempotent (if a PR already exists for the same branch, return it rather than creating a duplicate). `GetPRStatus` should retry on transient 404s (GitHub has a known ~500ms indexing delay after PR creation). `ValidateWebhook` must be constant-time to prevent timing attacks on HMAC verification.

| Implementation | Phase |
|---|---|
| `github` | Phase 1 |
| `gitlab` | Phase 2 |
| `bitbucket` | Phase 3+ |

**Manifest Update Strategy** (`pkg/update/strategy.go`): Rewrites artifact references in manifests.

```go
type Strategy interface {
    Update(ctx context.Context, dir string, artifacts []Artifact) error
    Name() string
}
```

Behavioral contract: `Update` must be idempotent. Re-running after a crash must produce the same output. Implementations must not create new files or delete existing ones, only modify in place.

| Implementation | Phase | Mechanism |
|---|---|---|
| `kustomize` | Phase 1 | `kustomize edit set-image` |
| `helm` | Phase 2 | Patch configurable path in `values.yaml` |
| `replace` | Phase 3 | Regex replacement in raw YAML |
| `oci` | Phase 4+ | Pull OCI artifact, unpack (for kro OCI artifacts when shipped) |

**Health Verification Adapter** (`pkg/health/adapter.go`): Verifies the GitOps tool applied the change.

```go
type Adapter interface {
    Check(ctx context.Context, opts CheckOptions) (HealthStatus, error)
    Name() string
    Available(ctx context.Context, client dynamic.Interface) (bool, error)
}
```

Behavioral contract: `Check` must not cache results across calls; each call must reflect current cluster state. `Available` must only check for CRD existence, not for specific resource instances.

**Delivery Delegate** (`pkg/delivery/delegate.go`): Watches progressive delivery status.

```go
type Delegate interface {
    Watch(ctx context.Context, opts WatchOptions) (<-chan RolloutStatus, error)
    Name() string
    Available(ctx context.Context, client dynamic.Interface) (bool, error)
}
```

| Implementation | Phase |
|---|---|
| `none` (default) | Phase 1 |
| `argoRollouts` | Phase 2 |
| `flagger` | Phase 2 |

**Metric Provider** (`pkg/metrics/provider.go`): Queries observability systems.

```go
type Provider interface {
    Query(ctx context.Context, query string, vars map[string]string) (float64, error)
    Name() string
}
```

**Artifact Source** (`pkg/source/watcher.go`): Watches registries for new artifacts.

```go
type Watcher interface {
    Discover(ctx context.Context, constraint string) ([]ArtifactVersion, error)
    Name() string
}
```

### 4.2 Provider Selection

The PromotionStep reconciler selects providers from CRD spec fields:

```
scm.GitClient + scm.SCMProvider  <--  pipeline.spec.git.provider ("github")
update.Strategy                  <--  env.update.strategy ("kustomize")
health.Adapter                   <--  env.health.type ("argocd", or auto-detected)
delivery.Delegate                <--  env.delivery.delegate ("argoRollouts")
```

The provider registry is a map from string identifier to constructor. Adding a provider: implement the interface, add `registry["gitea"] = gitea.New` to the registry initialization.

---

## 5. CRD API

### 5.1 Pipeline

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
  namespace: default
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    branch: main
    layout: directory
    provider: github
    secretRef: { name: github-token }
  environments:
    - name: dev
      path: environments/dev
      update: { strategy: kustomize }
      approval: auto
    - name: staging
      path: environments/staging
      update: { strategy: kustomize }
      approval: auto
    - name: prod
      path: environments/prod
      update: { strategy: kustomize }
      approval: pr-review
      health:
        timeout: 15m
      delivery:
        delegate: argoRollouts
  historyLimit: 20
```

Health check defaults: `kind: Deployment`, `name: <Pipeline.metadata.name>`, `namespace: <environment.name>`, `condition: Available`, `timeout: 10m`. Auto-detects Argo CD or Flux.

Health check types:
```yaml
health:
  type: argocd
  argocd: { name: my-app-prod, namespace: argocd }
# or
health:
  type: flux
  flux: { name: my-app-prod, namespace: flux-system }
# or for remote clusters:
health:
  type: argocd
  argocd: { name: my-app-prod-us-east }
  cluster: prod-us-east  # references a kubeconfig Secret
```

Environment ordering defaults to sequential. For parallel fan-out, use `dependsOn`:

```yaml
environments:
  - name: staging
  - name: prod-us-east
    dependsOn: [staging]
    approval: pr-review
  - name: prod-eu-west
    dependsOn: [staging]
    approval: pr-review
```

### 5.2 PolicyGate

Users define PolicyGate templates. The controller creates per-Bundle instances as Graph nodes.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend-deploys
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
    kardinal.io/type: gate
spec:
  expression: "!schedule.isWeekend"
  message: "Production deployments are blocked on weekends"
  recheckInterval: 5m
```

Per-Bundle instance status (written by kardinal-controller):

```yaml
status:
  ready: true
  lastEvaluatedAt: "2026-04-09T14:30:00Z"
  reason: "schedule.isWeekend = false"
```

CEL context attributes available by phase:

| Phase | Attributes |
|---|---|
| Phase 1 | `bundle.version`, `bundle.labels.*`, `bundle.provenance.*`, `bundle.intent.*`, `schedule.isWeekend`, `schedule.hour`, `schedule.dayOfWeek`, `environment.name`, `environment.approval` |
| Phase 2 | Phase 1 + `metrics.*`, `bundle.upstreamSoakMinutes`, `previousBundle.version` |
| Phase 3 | Phase 2 + `delegation.status`, `externalApproval.*` |
| Phase 4+ | Phase 3 + `contracts.*`, `targetDrift.unreconciled` |

Referencing attributes from a later phase causes a CEL evaluation error. The gate fails closed.

### 5.3 Generated Graph

For a Pipeline `[dev, staging, prod]` with two org prod gates, the controller generates:

```yaml
apiVersion: kro.run/v1alpha1
kind: Graph
metadata:
  name: my-app-v1-29-0
  ownerReferences:
    - apiVersion: kardinal.io/v1alpha1
      kind: Bundle
      name: my-app-v1-29-0-1712567890
spec:
  nodes:
    - id: bundle
      template:
        apiVersion: kardinal.io/v1alpha1
        kind: Bundle
        metadata:
          name: my-app-v1-29-0-1712567890

    - id: dev
      readyWhen:
        - ${dev.status.state == "Verified"}
      template:
        apiVersion: kardinal.io/v1alpha1
        kind: PromotionStep
        metadata:
          name: my-app-v1-29-0-dev
        spec:
          pipeline: my-app
          environment: dev
          bundleRef: ${bundle.metadata.name}
          path: environments/dev
          git:
            url: https://github.com/myorg/gitops-repo
            provider: github
            secretRef: github-token
          update: { strategy: kustomize }
          approval: auto

    - id: staging
      readyWhen:
        - ${staging.status.state == "Verified"}
      template:
        apiVersion: kardinal.io/v1alpha1
        kind: PromotionStep
        metadata:
          name: my-app-v1-29-0-staging
        spec:
          pipeline: my-app
          environment: staging
          bundleRef: ${bundle.metadata.name}
          path: environments/staging
          git:
            url: https://github.com/myorg/gitops-repo
            provider: github
            secretRef: github-token
          update: { strategy: kustomize }
          approval: auto
          upstreamVerified: ${dev.status.state}

    - id: noWeekendDeploys
      readyWhen:
        - ${noWeekendDeploys.status.ready == true}
        - ${timestamp(noWeekendDeploys.status.lastEvaluatedAt) > now() - duration("10m")}
      template:
        apiVersion: kardinal.io/v1alpha1
        kind: PolicyGate
        metadata:
          name: my-app-v1-29-0-no-weekend-deploys
        spec:
          expression: "!schedule.isWeekend"
          message: "Production deployments are blocked on weekends"
          recheckInterval: 5m
          upstreamEnvironment: ${staging.status.state}

    - id: stagingSoak
      readyWhen:
        - ${stagingSoak.status.ready == true}
        - ${timestamp(stagingSoak.status.lastEvaluatedAt) > now() - duration("2m")}
      template:
        apiVersion: kardinal.io/v1alpha1
        kind: PolicyGate
        metadata:
          name: my-app-v1-29-0-staging-soak
        spec:
          expression: "bundle.upstreamSoakMinutes >= 30"
          message: "Bundle must soak in staging for at least 30 minutes"
          recheckInterval: 1m
          upstreamEnvironment: ${staging.status.state}

    - id: prod
      readyWhen:
        - ${prod.status.state == "Verified"}
      template:
        apiVersion: kardinal.io/v1alpha1
        kind: PromotionStep
        metadata:
          name: my-app-v1-29-0-prod
        spec:
          pipeline: my-app
          environment: prod
          bundleRef: ${bundle.metadata.name}
          path: environments/prod
          git:
            url: https://github.com/myorg/gitops-repo
            provider: github
            secretRef: github-token
          update: { strategy: kustomize }
          approval: pr-review
          health:
            type: argocd
            argocd: { name: my-app-prod }
            timeout: 15m
          delivery:
            delegate: argoRollouts
          requiredGates:
            - ${noWeekendDeploys.metadata.name}
            - ${stagingSoak.metadata.name}
```

The `upstreamVerified`, `upstreamEnvironment`, and `requiredGates` fields serve dual purposes: they carry data the reconcilers need, and they create the CEL references that Graph uses for dependency inference.

### 5.4 PromotionStep

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PromotionStep
metadata:
  name: my-app-v1-29-0-prod
spec:
  pipeline: my-app
  environment: prod
  path: environments/prod
  bundleRef: my-app-v1-29-0-1712567890
  git:
    url: https://github.com/myorg/gitops-repo
    provider: github
    secretRef: github-token
  update: { strategy: kustomize }
  approval: pr-review
  health:
    type: argocd
    argocd: { name: my-app-prod, namespace: argocd }
    timeout: 15m
  delivery:
    delegate: argoRollouts
  upstreamVerified: "Verified"
  requiredGates: ["my-app-v1-29-0-no-weekend-deploys", "my-app-v1-29-0-staging-soak"]
status:
  state: Verified
  prURL: "https://github.com/myorg/gitops-repo/pull/144"
  mergedAt: "2026-04-09T11:15:00Z"
  verifiedAt: "2026-04-09T11:20:00Z"
  evidence:
    metrics: { success-rate: 0.997 }
    gateDuration: "4m12s"
    approvedBy: ["alice"]
    policyGates:
      - name: no-weekend-deploys
        result: pass
      - name: staging-soak
        result: pass
  delegatedTo: argoRollouts
  delegatedStatus: Healthy
```

### 5.5 Bundle

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: my-app-v1-29-0-1712567890
  namespace: default
  labels:
    kardinal.io/pipeline: my-app
  annotations:
    kardinal.io/pin: "false"
spec:
  artifacts:
    images:
      - name: my-app
        reference: ghcr.io/myorg/my-app:1.29.0
        digest: sha256:a1b2c3d4e5f6...
  provenance:
    commitSHA: "abc123def456"
    ciRunURL: "https://github.com/myorg/my-app/actions/runs/12345"
    author: "dependabot[bot]"
    buildTimestamp: "2026-04-09T10:00:00Z"
  intent:
    target: prod
status:
  phase: Verified
  graphRef: my-app-v1-29-0
  environments:
    dev:
      state: Verified
      promotedAt: "2026-04-09T10:05:00Z"
      verifiedAt: "2026-04-09T10:08:00Z"
    staging:
      state: Verified
      promotedAt: "2026-04-09T10:10:00Z"
      verifiedAt: "2026-04-09T10:18:00Z"
      evidence:
        metrics: { success-rate: 0.997 }
        gateDuration: "8m"
        approvedBy: []
        policyGates:
          - name: no-weekend-deploys
            result: pass
          - name: staging-soak
            result: pass
    prod:
      state: Verified
      promotedAt: "2026-04-09T10:20:00Z"
      verifiedAt: "2026-04-09T10:35:00Z"
      prURL: "https://github.com/myorg/gitops-repo/pull/144"
      evidence:
        metrics: { success-rate: 0.998 }
        gateDuration: "15m"
        approvedBy: ["alice"]
        policyGates:
          - name: no-weekend-deploys
            result: pass
          - name: staging-soak
            result: pass
```

Evidence is copied from PromotionStep `status.evidence` into Bundle `status.environments[].evidence` at verification time. This ensures the audit record survives PromotionStep garbage collection.

### 5.6 Bundle Intent: Skip Enforcement

Before generating the Graph, the controller validates `intent.skip`:

1. For each skipped environment, collect org-level PolicyGates with `kardinal.io/applies-to` matching that environment.
2. If any org gate applies to a skipped environment, the skip is denied unless a SkipPermission PolicyGate exists and evaluates to true:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-staging-skip-for-hotfix
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: staging
spec:
  expression: "bundle.labels.hotfix == true"
  message: "Hotfix bundles may skip staging"
```

SkipPermission is evaluated synchronously before Graph creation. If denied, Bundle status is set to `SkipDenied` with a reason message.

### 5.7 MetricCheck (Phase 2)

```yaml
apiVersion: kardinal.io/v1alpha1
kind: MetricCheck
metadata:
  name: success-rate
spec:
  provider: { type: prometheus, address: "http://prometheus:9090" }
  query: |
    sum(rate(http_requests_total{app="{{ .AppName }}",status!~"5.."}[5m]))
    / sum(rate(http_requests_total{app="{{ .AppName }}"}[5m]))
  successCondition: ">= 0.99"
  failureCondition: "< 0.95"
  noDataBehavior: hold
```

### 5.8 kardinal init

Input:
```yaml
app: my-app
image: ghcr.io/myorg/my-app
git:
  url: https://github.com/myorg/gitops-repo
  secret: github-token
environments: [dev, staging, prod]
prodApproval: pr-review
```

Output:
```
$ kardinal init -f kardinal.yaml
Pipeline "my-app" created (3 environments: dev, staging, prod)
  2 org PolicyGates will be injected: no-weekend-deploys, staging-soak

Next: Add to your CI:
  kardinal create bundle my-app --image ghcr.io/myorg/my-app:$TAG
```

---

## 6. CLI

### Phase 1

| Command | Description |
|---|---|
| `kardinal init [-f config.yaml]` | Generate Pipeline from simple config |
| `kardinal get pipelines` | List Pipelines with current Bundle per environment |
| `kardinal get steps <pipeline>` | Show PromotionSteps and PolicyGates with state |
| `kardinal get bundles <pipeline>` | List Bundles with provenance |
| `kardinal create bundle <pipeline>` | Create Bundle CRD |
| `kardinal promote <pipeline> --env <env>` | Manually trigger promotion |
| `kardinal explain <pipeline> --env <env>` | PolicyGate evaluation trace with current values |
| `kardinal explain <pipeline> --env <env> --watch` | Continuous output, re-evaluates on change |
| `kardinal version` | CLI and controller versions |

### Phase 2

| Command | Description |
|---|---|
| `kardinal rollback <pipeline> --env <env>` | Promote previous Bundle |
| `kardinal rollback ... --emergency` | Emergency rollback with priority label |
| `kardinal pause <pipeline>` | Inject PolicyGate with `expression: "false"` |
| `kardinal resume <pipeline>` | Remove freeze gate |
| `kardinal history <pipeline>` | Promotion history with evidence |
| `kardinal policy list` | List PolicyGate CRDs |
| `kardinal policy test <file>` | Validate CEL against context schema |
| `kardinal policy simulate --env prod --time "Saturday 3pm"` | Simulate gate evaluation against hypothetical context |
| `kardinal diff <bundle-a> <bundle-b>` | Artifact diff |

`kardinal explain` example output:

```
PROMOTION: my-app / prod
  Bundle: v1.29.0

POLICY GATES:
  no-weekend-deploys  [org]   PASS   schedule.isWeekend = false
  staging-soak        [org]   FAIL   bundle.upstreamSoakMinutes = 12 (threshold: >= 30)
                                     ETA: ~18 minutes (based on staging verifiedAt)

RESULT: BLOCKED by staging-soak
```

Phase 2 adds ETA estimation for time-based gates by computing the delta between the threshold and the current value against the known staging verification timestamp.

---

## 7. PR Approval Flow

For `approval: pr-review` environments, the controller opens a PR with a structured body:

```markdown
## Promotion: my-app v1.29.0 to prod

### Policy Gates
| Gate | Scope | Status | Detail |
|---|---|---|---|
| no-weekend-deploys | org | PASS | Tuesday 14:00 UTC |
| staging-soak | org | PASS | Soak: 45m (min: 30m) |

### Artifact
| Field | Value |
|---|---|
| Image | ghcr.io/myorg/my-app:1.29.0 |
| Digest | sha256:a1b2c3d4 |
| Source Commit | abc123d |
| CI Run | Build #12345 |

### Upstream Verification
| Environment | Verified | Soak |
|---|---|---|
| dev | 2h ago | n/a |
| staging | 45m ago | 45m |

### Changes
ghcr.io/myorg/my-app: 1.28.0 to 1.29.0
```

For `approval: auto` environments: direct push to the target branch, no PR. Add `pr: true` on the environment config if an audit trail PR is desired.

Merge detection: webhook primary (`/webhooks` endpoint), 5m background sweep fallback.

Mid-flight policy changes: new PolicyGates added after a Graph is created do not apply to that Graph. They apply to all subsequent Bundles. Use `kardinal pause` to block in-flight promotions.

---

## 8. Rollback

Rollback creates a new Bundle whose `spec.artifacts` point to the previous verified version. This Bundle runs through the same pipeline, same Graph generation, same PolicyGate evaluation, same PR flow. There is no separate rollback mechanism.

`kardinal rollback <pipeline> --env prod` creates a Bundle with the prior version and `intent.target: prod`. The `kardinal/rollback` label is added to the PR for visibility.

On in-flight failure (PromotionStep `status.state = "Failed"`), Graph stops all downstream nodes. The controller opens rollback PRs for environments that received the failed Bundle.

---

## 9. Comparison

| Dimension | kardinal-promoter | Kargo (v1.9.5) | GitOps Promoter (v0.26.3) |
|---|---|---|---|
| Pipeline model | Graph DAG (fan-out, conditional, forEach) | Stage dependencies (configurable topology) | Linear branch promotion |
| Policy governance | PolicyGate Graph nodes (CEL), visible in UI | None shipped; approval policies planned (#3440) | Commit statuses |
| GitOps integration | Auto-detects Argo CD, Flux, bare K8s | Argo CD for health verification; Git steps are provider-agnostic | Any (commit statuses) |
| PR approval | Promotion evidence (provenance, upstream metrics, policy table) | git-open-pr / wait-for-pr steps (since v1.8.0) | PR with branch diff |
| Artifact bundling | Bundle CRD (images, Helm, Git ref, provenance) | Freight (images, charts, Git refs, digests) | None (raw Git diff) |
| Build provenance | Commit SHA, CI run, author, digest | Image digests | None |
| Rollback | Forward promotion of prior Bundle through same pipeline | Re-promote prior Freight; AnalysisTemplate verification | Revert PR manually |
| Multi-cluster | Argo CD hub-spoke, Flux via kubeconfig, bare K8s via kubeconfig | Argo CD hub-spoke | Branch structure |
| Artifact source watching | Phase 3 (Subscription CRD). Phase 1 uses CI webhook. | Day 1 (Warehouse auto-discovers images, charts, Git) | Commit on source branch |
| Git providers | GitHub (Phase 1), GitLab (Phase 2) | GitHub, GitLab, Gitea, BitBucket | GitHub, GitLab, Bitbucket, Gitea, Forgejo |
| Update strategies | Kustomize (Phase 1), Helm (Phase 2) | Kustomize, Helm, YAML, pluggable step runner | N/A (promotes branches) |
| UI | kardinal-ui (embedded, Phase 1b) | Production-grade promotion board (React + Ant Design) | Lightweight UI + Argo CD extension |
| Production users | None | Multiple (enterprise) | Intuit |
| Maturity | Pre-release | v1.9.x | v0.26.x |

---

## 10. Non-Goals

| Out of scope | Rationale |
|---|---|
| Traffic management | Delegated to Argo Rollouts or Flagger |
| GitOps sync | The controller writes to Git. GitOps tools sync to the cluster. |
| CI pipelines | CI creates Bundles. The controller does not build images or run tests. |
| Secret management | Managed in the GitOps repo per environment directory. |
| Native canary or blue-green | Delegated. The controller never mutates workload resources. |
| External database | Kubernetes is the database. |
| Graph primitive development | The controller uses Graph. The kro team maintains Graph. |

---

## 11. MVP Scope

### Phase 1a: Weeks 1-6 (core engine)

| Feature | Detail |
|---|---|
| Pipeline CRD | Linear + dependsOn, generates Graph with PolicyGate injection |
| Graph generation | Per-Bundle Graph from Pipeline spec + intent |
| PromotionStep CRD | Execution unit, created by Graph controller |
| PolicyGate CRD | CEL-powered Graph nodes, lastEvaluatedAt race prevention, timer-based recheck |
| Bundle CRD | Intent, provenance, pin, skip-permission enforcement, evidence in status |
| Bundle creation | Webhook + CLI + kubectl apply |
| PR-based promotion | GitHub PRs with evidence and policy gate table |
| Kustomize update | kustomize edit set-image |
| Health: Deployment | resource adapter |
| CLI | get pipelines/steps/bundles, create bundle, promote, explain, explain --watch, version |
| Git caching | Shared work trees per repo URL |
| Helm chart | Controller installation (Graph controller assumed available) |
| E2E test | Kind cluster + Graph controller + GitHub repo + 3-env pipeline + PolicyGate blocking |

### Phase 1b: Weeks 7-12 (UI, GitOps integration, multi-cluster)

| Feature | Detail |
|---|---|
| kardinal-ui | Embedded UI: promotion DAG, policy nodes, provenance, PR links |
| Health: Argo CD | argocd adapter (auto-detected) |
| Health: Flux | flux adapter (auto-detected) |
| Health: remote cluster | cluster field referencing kubeconfig Secret |
| kardinal init | Generate Pipeline from 8-line config |
| GitHub Action | kardinal-dev/create-bundle-action |
| Controller metrics | /metrics endpoint |
| Webhook auth | X-Hub-Signature-256 (SCM) + Bearer+HMAC (Bundles) |
| --ui-listen-address | Separate UI port from API port |

### Phase 2: Weeks 13-20

| Feature | Detail |
|---|---|
| Rollback CLI + automatic rollback on failure | |
| Delegation: Argo Rollouts + Flagger | argoRollouts and flagger health adapters |
| MetricCheck CRD | Prometheus provider |
| Metric-based CEL context | metrics.* in PolicyGate expressions |
| Promotion evidence | Metrics, gate duration, approver recorded in Bundle status |
| kardinal policy simulate | Simulate gates against hypothetical conditions |
| kardinal explain ETA | Estimated time to pass for time-based gates |
| kardinal pause / resume | Inject/remove freeze PolicyGate |
| Bundle superseding with pin | |
| GitLab support | gitlab SCM provider |
| Helm values update | helm update strategy |

### Phase 3: Weeks 21-28

| Feature | Detail |
|---|---|
| Subscription CRD | Declarative registry watcher, auto-creates Bundles |
| Direct Graph authoring | Power users write Graphs for fan-out, conditional steps |
| Webhook gate | externalApproval.* in CEL context |
| GitHub App auth | |
| Security hardening | RBAC tightening, Pod security, network policy docs |
| Documentation site | Quickstart, guides, API reference |

### Phase 4+

| Feature | Detail |
|---|---|
| PipelineGroup | Multi-service Graphs with cross-service ordering |
| Promotion Contracts | Cross-pipeline PolicyGates |
| Drift-aware gates | targetDrift.unreconciled in CEL context |
| Promotion Timeline API | Queryable audit trail for compliance reporting |

### Testing Tiers

| Tier | Scope | CI |
|---|---|---|
| 1 | Pipeline + Graph + GitHub + Kustomize + PolicyGates + Deployment health | Every PR |
| 2 | Direct Graph, GitLab, Helm, metrics, delegation | Nightly |
| 3 | Fan-out, conditional steps, Subscription, webhook gates | Release |

---

## 12. Open Questions

| # | Question | Default |
|---|---|---|
| 1 | Graph controller availability | Assumed pre-installed. How Graph gets installed is outside the scope of this document. |
| 2 | Org gate bypass prevention | Org PolicyGates are injected as mandatory Graph dependencies. The Pipeline CRD has no mechanism to exclude them. |
| 3 | PolicyGate namespace scanning | `--policy-namespaces=platform-policies` flag. Pipeline namespace always included. |
| 4 | Default when no PolicyGates exist | No gates injected. Promotion proceeds based on the approval field. |
| 5 | kardinal pause mechanism | Creates a PolicyGate with `expression: "false"`. `resume` deletes it. |
| 6 | PolicyGates with metrics | Phase 2. MetricCheck results injected into CEL context as `metrics.*`. |
| 7 | Pipeline to Graph export | `kardinal init --mode graph --from my-app` generates the Graph spec for direct authoring. |
| 8 | Bundle status scaling | Evidence is approximately 5-8KB per Bundle at 10 environments. At 50+ environments, consider a separate PromotionRecord CRD. |
| 9 | Git scaling limits | Designed for fewer than 50 pipelines writing to fewer than 5 Git repos. At larger scale, use multiple kardinal instances or queue-based write serialization. |
| 10 | Mid-flight policy changes | New gates do not apply to existing Graphs. Use `kardinal pause` for immediate block on in-flight promotions. |
| 11 | Graph dependency edges | PromotionStep and PolicyGate specs carry fields that reference upstream nodes via CEL. These fields serve reconciler purposes and also create Graph dependency edges. A native `dependsOn` on Graph nodes is proposed as a contribution (Section 17). |
| 12 | PR comment staleness | PR comments are snapshots updated on webhook events. Between updates, they may show stale gate state. The UI and kubectl show live state. This is documented in Section 3.7. |

---

## 13. Security

### RBAC

| Resource | Verbs | Purpose |
|---|---|---|
| pipelines.kardinal.io | get, list, watch, create, update, delete | Pipeline management |
| promotionsteps.kardinal.io | get, list, watch, create, update | Step lifecycle |
| policygates.kardinal.io | get, list, watch | Controller reads. Create/update restricted to platform-policies namespace via RBAC. |
| bundles.kardinal.io | get, list, watch, create, update, delete | Bundle management |
| graphs.kro.run | get, list, watch, create, update, delete | Graph lifecycle |
| graphrevisions.internal.kro.run | get, list, watch | Revision tracking |
| deployments.apps | get, list, watch | Health (resource adapter) |
| applications.argoproj.io | get, list, watch | Health (argocd adapter) |
| kustomizations.kustomize.toolkit.fluxcd.io | get, list, watch | Health (flux adapter) |
| rollouts.argoproj.io | get, list, watch | Delegation (Phase 2) |
| canaries.flagger.app | get, list, watch | Delegation (Phase 2) |
| secrets | get | kubeconfig for remote clusters |
| leases | get, create, update | Leader election |

### Webhook Authentication

- `/webhooks` (SCM events): `X-Hub-Signature-256` for GitHub, `X-Gitlab-Token` for GitLab. Constant-time comparison.
- `/api/v1/bundles` (CI events): Bearer token + HMAC signature. Rate limited at 100 requests per minute per Pipeline.

### Pod Security

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 65534
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities:
    drop: [ALL]
```

Git cache at `/var/cache/kardinal/` uses an emptyDir volume.

---

## 14. Failure Modes

| Scenario | Behavior |
|---|---|
| Controller restart mid-promotion | Reads CRD status from etcd. Resumes from last known state. Duplicate PR prevention via existing-PR check before creation. |
| Controller restart mid-delegation | Reads Rollout or Canary status from cluster. Resumes watching. |
| Graph controller down | Graph paused. Existing PromotionStep and PolicyGate CRs continue being reconciled by kardinal-controller. Graph resumes on restart. |
| Argo CD Application not found | Falls back to resource adapter. Warning event logged on PromotionStep. |
| Flux Kustomization stuck reconciling | Health check waits until timeout, then marks the step as Failed. |
| Leader election | Lease-based. One active replica, one standby. 15s failover. |
| Git rate limit | Exponential backoff from 1s to 5m maximum. |
| PR creation fails | 3 retries with exponential backoff. |
| SCM webhook missed | Background sweep catches open PRs within 5m. |
| CEL evaluation error | PolicyGate set to not-ready. Fail-closed. |
| PolicyGate stale after restart | lastEvaluatedAt freshness check in Graph readyWhen. Treated as not-ready until re-evaluated. |
| PolicyGate timer-based recheck fails | Gate stays in last-known state. Graph does not advance (stale lastEvaluatedAt). Resumes on next successful evaluation. |

---

## 15. Prerequisites

**Graph controller.** kro's Graph controller must be available in the cluster, providing the Graph and GraphRevision CRDs and the DAG reconciliation engine.

**GitOps repository.** Directory-per-environment layout with deployable manifests. Environment-specific configuration (secrets, resource limits, env vars) must already be managed per directory.

**GitOps tool.** Argo CD, Flux, or equivalent syncing each environment directory to the target cluster.

**CI integration.** CI pipeline calls `/api/v1/bundles`, uses `kardinal-dev/create-bundle-action`, or applies Bundle CRDs via kubectl. Phase 3 Subscription CRD removes the CI requirement.

**Progressive delivery (optional).** Argo Rollouts or Flagger for canary or blue-green delivery.

---

## 16. Graph Gaps and Proposed Contributions

The Graph primitive is under active development. During kardinal-promoter implementation, we expect to identify gaps and contribute fixes upstream.

| Gap | Impact on kardinal-promoter | Current workaround | Proposed contribution |
|---|---|---|---|
| No timer-based re-evaluation | Time-based PolicyGates (schedule checks, soak time) do not re-evaluate without external stimulus | kardinal-controller runs a timer loop that writes status updates at recheckInterval, triggering Graph watch events | Add `recheckAfter` hint on Graph nodes, similar to RequeueAfter in controller-runtime |
| No explicit dependsOn on nodes | Dependencies must be created via CEL field references in templates | PromotionStep and PolicyGate specs include fields (`upstreamVerified`, `upstreamEnvironment`, `requiredGates`) that reference upstream nodes. These fields serve reconciler purposes and create edges as a secondary effect. | Add optional `dependsOn` to Graph node spec for structural ordering without requiring data flow |
| Partial DAG instantiation | Bundle intent.target limits which environments are included | Handled in the Pipeline-to-Graph translation layer (only include nodes up to target) | No Graph change needed |
| Node-level status surface | kardinal-ui needs per-node promotion context beyond Graph readyWhen | Read child CRD status directly (PromotionStep, PolicyGate) | No Graph change needed |
