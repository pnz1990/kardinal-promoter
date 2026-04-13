# Architecture

kardinal-promoter is a Kubernetes-native controller. All state lives in etcd as CRDs. The CLI, UI, and webhook API are convenience layers that create and read CRDs — you can operate the entire system with `kubectl`.

---

## System Overview

```mermaid
graph TD
    CI["CI/CD Pipeline<br/>(GitHub Actions, etc.)"] -->|"POST /api/v1/bundles<br/>or kubectl apply"| Bundle

    subgraph "Kubernetes Cluster"
        Bundle["Bundle CRD<br/>image ref + provenance"]
        Pipeline["Pipeline CRD<br/>environments + update strategy"]
        PolicyGate["PolicyGate CRDs<br/>CEL expressions"]

        Bundle -->|"translates to"| Graph["kro Graph<br/>(per-Bundle DAG)"]
        Pipeline -->|"read by translator"| Graph
        PolicyGate -->|"injected as DAG nodes"| Graph

        Graph -->|"creates"| PS["PromotionStep CRs<br/>(per environment)"]
        Graph -->|"creates"| PGI["PolicyGate instances"]

        PS -->|"executes"| Steps["Steps Engine<br/>image update → PR → health-check"]
        PGI -->|"evaluates CEL"| PGR["status.ready = true/false"]

        PGR -->|"readyWhen"| GraphAdv["Graph advances<br/>to next environment"]
        Steps -->|"Verified"| GraphAdv

        Steps -->|"Failed"| Rollback["Rollback PR<br/>(kardinal/rollback label)"]
    end

    kubectl["kubectl / kardinal CLI / UI"] -->|"reads/writes"| Bundle
    kubectl -->|"reads"| PS
    kubectl -->|"reads"| PGI
```

---

## Core Components

### Controller (`cmd/kardinal-controller`)

The controller manager runs three reconcilers:

| Reconciler | CRD | Responsibility |
|---|---|---|
| `BundleReconciler` | `Bundle` | Calls the translator to build a kro Graph; watches Graph status |
| `PromotionStepReconciler` | `PromotionStep` | Runs the steps engine: git-clone → image update → PR → health check |
| `PolicyGateReconciler` | `PolicyGate` (instances) | Evaluates CEL expression; writes `status.ready` |
| `MetricCheckReconciler` | `MetricCheck` | Queries Prometheus; writes result to status |

### Translator (`pkg/translator`)

Converts a `Pipeline` CRD + a `Bundle` CRD into a [kro](https://github.com/kubernetes-sigs/kro) `Graph` spec. The Graph encodes the full promotion DAG:

- One node per `PromotionStep` (environment)
- One node per `PolicyGate` instance (injected between environments)
- `readyWhen` expressions wired so the Graph controller advances nodes in dependency order

### kro Graph Controller (`kro-system`)

kardinal-promoter does **not** implement graph coordination itself. It delegates to the
krocodile Graph controller (an experimental fork of [kro](https://github.com/kubernetes-sigs/kro))
which manages the DAG lifecycle:

- Creates owned resources (PromotionStep CRs, PolicyGate CRs) in topological order
- Advances to the next node when `readyWhen` is satisfied
- Stops the DAG on failure, preventing downstream promotions

> **Dependency note**: kardinal-promoter requires krocodile to be installed in the cluster.
> See [Installation](installation.md#install-krocodile) for setup.

### Steps Engine (`pkg/steps`)

The `PromotionStepReconciler` runs a sequence of built-in steps for each environment:

```
kustomize-set-image  →  git-commit  →  open-pr  →  wait-for-merge  →  health-check
```

Built-in step implementations:

| Step | Description |
|---|---|
| `kustomize-set-image` | Runs `kustomize edit set image` and writes the result to a new Git branch |
| `helm-set-image` | Updates `values.yaml` image tag for Helm-based repos |
| `git-commit` | Commits changes to the environment branch |
| `open-pr` | Opens a pull request via the SCM provider |
| `wait-for-merge` | Polls the PR until it is merged or closed |
| `health-check` | Queries Kubernetes Deployment readiness or ArgoCD/Flux sync status |
| `metric-check` | Evaluates a PromQL query against Prometheus (uses `MetricCheck` CRD) |
| `http-check` | Calls a configurable HTTP endpoint and checks the response |
| `custom-step` | Calls a user-defined webhook with the promotion context |

### PolicyGate Evaluator (`pkg/reconciler/policygate`)

Evaluates CEL expressions against the promotion context. Uses the
[kro CEL library](https://github.com/kubernetes-sigs/kro/tree/main/pkg/cel/library),
giving gates access to `json.*`, `maps.*`, `lists.*`, `random.*`, and standard string
extension functions.

See [CEL Context Reference](reference/cel-context.md) for the full variable list.

### SCM Provider (`pkg/scm`)

Abstracts Git hosting operations. Current implementations:

| Provider | Status |
|---|---|
| GitHub | GA |
| GitLab | Beta |

### Health Adapters (`pkg/health`)

Checks whether a promotion is healthy after merging:

| Adapter | Status |
|---|---|
| Kubernetes `Deployment` readiness | GA |
| ArgoCD `Application` sync status | GA |
| Argo Rollouts `Rollout` status | Beta |
| Flux `Kustomization` ready status | GA |

---

## Data Flow: Bundle → Verified

```mermaid
sequenceDiagram
    participant CI
    participant API as kardinal API
    participant K8s as Kubernetes API Server
    participant Bundle as BundleReconciler
    participant kro as krocodile Graph
    participant PS as PromotionStepReconciler
    participant PG as PolicyGateReconciler

    CI->>API: create Bundle (image + pipeline)
    API->>K8s: create Bundle CR
    K8s->>Bundle: reconcile event
    Bundle->>K8s: create Graph CR (DAG spec)
    kro->>K8s: create PromotionStep[test] + PolicyGate instances
    K8s->>PG: reconcile PolicyGate[test]
    PG->>K8s: status.ready = true (CEL passed)
    kro->>K8s: advance DAG → create PromotionStep[test] steps
    K8s->>PS: reconcile PromotionStep[test]
    PS->>PS: image update → commit → open PR → wait merge → health check
    PS->>K8s: status.phase = Verified
    kro->>kro: readyWhen satisfied → advance to uat
    Note over kro,PS: Repeat for uat → prod
    kro->>K8s: Graph.status.state = Verified
    K8s->>Bundle: status.phase = Verified
```

---

## How kardinal Relates to ArgoCD and Flux

kardinal-promoter is **GitOps-agnostic**: it does not communicate with ArgoCD or Flux
during promotion. Instead:

1. `PromotionStepReconciler` opens a Git pull request with the updated image reference.
2. A human (or automated process) merges the PR.
3. ArgoCD or Flux detects the Git change and syncs the cluster.
4. kardinal's **health check** then queries ArgoCD or Flux to confirm sync is complete
   before advancing the DAG.

This means kardinal works with any GitOps engine — or even without one (raw Kubernetes deployments).

---

## State Management

All state is stored in Kubernetes CRDs:

| CRD | Purpose |
|---|---|
| `Pipeline` | Defines environments, update strategy, SCM config |
| `Bundle` | Immutable deployment unit; created by CI |
| `PromotionStep` | Per-environment promotion progress; owned by Graph |
| `PolicyGate` | Policy check template (cluster-scoped or namespace-scoped) |
| `PRStatus` | Tracks GitHub PR open/merged/closed state |
| `RollbackPolicy` | Auto-rollback configuration for a Pipeline |
| `MetricCheck` | Prometheus query check, created as a DAG node |

The controller is **stateless**: it can be restarted at any time without data loss. All
state is recovered by re-reading CRDs.

---

## Further Reading

- [Concepts](concepts.md) — Bundles, Pipelines, PolicyGates explained
- [Policy Gates](policy-gates.md) — CEL expression reference
- [CEL Context Reference](reference/cel-context.md) — variables available in gate expressions
- [Installation](installation.md) — how to install kardinal-promoter
