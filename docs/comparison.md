# Comparison: kardinal vs Kargo vs GitOps Promoter

This page compares kardinal-promoter with the two most similar tools in the GitOps promotion space.

!!! note "Objectivity"
    This comparison is based on publicly available documentation and source code as of April 2026.
    All three tools are actively developed. Check each project's releases for the latest capabilities.

---

## Feature Matrix

| Feature | kardinal-promoter | Kargo | GitOps Promoter |
|---|---|---|---|
| **Promotion model** | DAG (fan-out, arbitrary dependencies) | Linear pipeline | Linear pipeline |
| **Parallel environments** | Yes — fan-out to prod-us, prod-eu simultaneously | No | No |
| **Policy gates** | CEL expressions (kro library: json, maps, schedule, upstream) | Approval-only (PR/manual) | Basic webhook-based |
| **Policy expressiveness** | Full CEL: time-based, annotation-based, soak-time, upstream metrics | Limited | Very limited |
| **PR evidence body** | Structured (image digest, CI run, gate results, soak time) | None | Minimal |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes | ArgoCD only | Flux only |
| **SCM providers** | GitHub (GA), GitLab (Beta) | GitHub, GitLab | GitHub |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts | ArgoCD Application | Flux Kustomization |
| **Rollback mechanism** | Promotion of previous artifact through same pipeline | Manual | Manual |
| **Auto-rollback** | Yes (configurable failure threshold + `RollbackPolicy` CRD) | No | No |
| **CLI** | `kardinal` CLI with `get/explain/rollback/approve/simulate` | `kargo` CLI | No CLI |
| **UI dashboard** | Embedded React UI (DAG visualization, gate states) | Kargo UI | No |
| **Metric-gated promotions** | Yes (Prometheus/PromQL via `MetricCheck` CRD) | No | No |
| **Multi-cluster promotions** | Yes (configured via Pipeline CRD) | Yes | Yes |
| **CRD-based configuration** | Yes — Pipeline, Bundle, PolicyGate, RollbackPolicy | Yes | Yes |
| **Architecture** | Graph-first (krocodile DAG) | Controller-first | Controller-first |
| **Maturity** | Pre-1.0, active development | v1.0+, production-grade | Pre-1.0 |
| **License** | Apache 2.0 | Apache 2.0 | Apache 2.0 |

---

## Promotion Model

### kardinal: DAG Pipelines

kardinal-promoter models each promotion as a **directed acyclic graph**. This enables:

- **Fan-out**: promote to `prod-us` and `prod-eu` in parallel, gate final verification on both completing
- **Conditional paths**: skip certain environments based on Bundle annotations or gate results
- **Complex dependencies**: require `canary-eu` to soak for 30 minutes before allowing `prod-eu`

```
test ──► uat ──► staging ──┬──► prod-us ──┐
                           └──► prod-eu ──┴──► verified
```

### Kargo: Linear Stages

Kargo uses a fixed chain of **stages** (called a Warehouse → Stage pipeline). Promotions
flow sequentially from one stage to the next. Fan-out is not natively supported.

### GitOps Promoter: Linear Environments

GitOps Promoter promotes commits sequentially through a defined list of environments.
No branching or parallel promotion.

---

## Policy Gates

### kardinal: CEL with kro library

PolicyGate expressions use the [kro CEL library](https://github.com/kubernetes-sigs/kro/tree/main/pkg/cel/library):

```yaml
# Block weekend deploys
expression: "!schedule.isWeekend()"

# Require upstream soak time
expression: "upstream.uat.soakMinutes >= 30"

# Check bundle annotation
expression: 'bundle.metadata.annotations["release-type"] != "hotfix" || upstream.uat.soakMinutes >= 5'

# Query upstream metrics
expression: "metrics.errorRate < 0.01"
```

Gates are Kubernetes CRDs, scoped to org (mandatory) or team (additive). They are
visible in the CLI (`kardinal explain`) and the embedded UI.

### Kargo: Approval-only Gates

Kargo requires manual approval to advance between stages. There is no expression-based
policy engine in the core product.

### GitOps Promoter: Webhook-based

GitOps Promoter supports webhook-based checks that can block promotion. The check
logic lives outside the tool (in a custom webhook server). Less integrated, but
more flexible for teams with existing automation.

---

## PR Evidence

### kardinal: Structured Evidence Body

Every production PR opened by kardinal includes a structured body:

```markdown
## Promotion Evidence

**Pipeline**: my-app
**Bundle**: v1.29.0
**Image**: ghcr.io/myorg/my-app@sha256:abc123...

### Gate Results
| Gate | Result | Expression |
|---|---|---|
| no-weekend-deploys | ✅ PASS | `!schedule.isWeekend()` |
| require-uat-soak | ✅ PASS | `upstream.uat.soakMinutes >= 30` |

### Upstream Environments
| Env | Status | Verified At |
|---|---|---|
| test | Verified | 2026-04-13 09:00 UTC |
| uat | Verified | 2026-04-13 09:45 UTC |
```

### Kargo: No PR Evidence

Kargo does not open PRs with promotion evidence. Promotion is tracked in the Kargo UI.

### GitOps Promoter: Minimal PR

GitOps Promoter opens PRs but with minimal context — the Git diff only.

---

## When to Choose Each Tool

### Choose kardinal-promoter if:

- You need **parallel environment promotions** (multi-region, multi-cluster fan-out)
- You want **expressive policy gates** (time-based, soak-time, metric-gated) without writing webhook servers
- You use **both ArgoCD and Flux** (or neither) — and don't want to commit to one GitOps engine
- You want **structured PR evidence** that gives reviewers full promotion context
- You want **auto-rollback** triggered by health check failures

### Choose Kargo if:

- You are already deeply invested in **ArgoCD** and want native integration
- Your promotion pipelines are **simple linear chains** without complex gates
- You need **production-grade maturity** (v1.0+ releases, larger community)
- You prefer the **Kargo UI** and existing ecosystem

### Choose GitOps Promoter if:

- You are committed to **Flux** as your GitOps engine
- You want minimal opinions — just "promote this commit forward"
- You have existing webhook infrastructure for custom checks

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](faq.md) — common questions
