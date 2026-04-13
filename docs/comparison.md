# Comparison: kardinal vs Kargo vs GitOps Promoter

This page compares kardinal-promoter with the two most similar tools in the GitOps promotion space.

!!! note "Objectivity"
    This comparison is based on publicly available documentation and source code as of April 2026.
    All three tools are actively developed. Check each project's releases for the latest capabilities.

---

## Feature Matrix

| Feature | kardinal-promoter | Kargo | GitOps Promoter |
|---|---|---|---|
| **Promotion model** | DAG (fan-out, arbitrary dependencies) | Stage pipeline | Linear pipeline |
| **Parallel environments** | Yes — native fan-out | No | No — DAG on roadmap |
| **Policy gates** | CEL (kro library: schedule, upstream soak, metrics, cross-stage) | Manual approval only | CommitStatus-based webhook checks |
| **Cross-stage policy** | Yes — gate can read upstream soak, history, metrics | No | No |
| **PR evidence body** | Structured (image digest, CI run, gate results, soak time) | None — tracked in Kargo UI | Git diff only |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes | ArgoCD (primary), others partial | ArgoCD, Flux, any |
| **SCM providers** | GitHub, GitLab | GitHub, GitLab | GitHub |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts, Flagger | ArgoCD Application | ArgoCD Application |
| **Rollback mechanism** | Promotion of previous artifact through same pipeline | Manual | Manual git revert |
| **Auto-rollback** | Yes (`RollbackPolicy` CRD, configurable threshold) | No | No |
| **CLI** | Full `kardinal` CLI | `kargo` CLI | No CLI |
| **UI dashboard** | Embedded React UI (DAG visualization, gate states) | Polished Kargo UI | No UI |
| **Metric-gated promotions** | Yes (`MetricCheck` CRD + PromQL) | No | No |
| **Multi-cluster** | Yes (Pipeline CRD) | Yes | Yes |
| **Upstream soak time in gates** | Yes — `bundle.upstreamSoakMinutes >= 30` | No | Timed controller (elapsed only) |
| **Artifact discovery** | Bundle created by CI/CLI | Warehouse (automatic OCI/git scanning) | Git commit-based |
| **Multi-artifact bundle** | Yes (image + config in one Bundle) | Yes (Freight) | No |
| **Architecture** | Graph-first (krocodile DAG) | Stage/controller | Controller |
| **Maturity** | v0.4.0, active development | v1.9.x, production-grade | v0.26.x, experimental |
| **License** | Apache 2.0 | Apache 2.0 | Apache 2.0 |

---

## Where kardinal Leads

### Graph-native policy evaluation

kardinal PolicyGates are nodes in the kro DAG. They have access to the entire pipeline's
state — not just the current stage:

```yaml
# In a prod PolicyGate — reads upstream uat stage's soak time
expression: "bundle.upstreamSoakMinutes >= 60"

# Read upstream metrics
expression: "metrics.errorRate < 0.01"

# Combine schedule + soak + metadata
expression: '!schedule.isWeekend && bundle.upstreamSoakMinutes >= 30 && bundle.labels.hotfix != "true"'
```

Neither Kargo nor GitOps Promoter can express "do not promote to prod unless UAT has been
healthy for 60 minutes." Kargo has per-stage approval with no expression engine. GitOps
Promoter has webhook-based checks with no pipeline context.

### Fan-out DAG topology

```
test ──► uat ──► staging ──┬──► prod-us ──┐
                           └──► prod-eu ──┴──► verified
```

kardinal models this natively. Kargo is sequential. GitOps Promoter has no DAG (roadmap item).

### Structured PR evidence

Every promotion PR opened by kardinal includes:

```markdown
## Promotion Evidence

**Pipeline**: my-app  **Bundle**: sha-9349a3f
**Image**: ghcr.io/pnz1990/kardinal-test-app@sha256:abc123...

### Gate Results
| Gate | Result | Expression |
|---|---|---|
| no-weekend-deploys | ✅ PASS | `!schedule.isWeekend` |
| require-uat-soak   | ✅ PASS | `bundle.upstreamSoakMinutes >= 30 (actual: 47)` |

### Upstream Environments
| Env | Status | Verified At |
|---|---|---|
| test | Verified | 2026-04-13 09:00 UTC |
| uat  | Verified | 2026-04-13 09:45 UTC |
```

Kargo tracks promotions in its own UI — PRs have no evidence body. GitOps Promoter
PRs show the git diff only.

### Auto-rollback

kardinal opens a rollback PR automatically when a promotion fails health verification,
using the previous Bundle. `RollbackPolicy` CRD controls the threshold and strategy.
Neither competitor has automated rollback.

---

## Where Kargo Leads

**Artifact discovery**: Kargo's Warehouse concept automatically monitors OCI registries
and Git repos for new artifact versions, packages them as Freight, and feeds them into
the pipeline. kardinal requires CI to create Bundles explicitly (via webhook or CLI).
If you want passive artifact detection without modifying CI pipelines, Kargo wins.

**Production maturity**: Kargo is at v1.9.x with commercial support from Akuity,
a larger community, and a longer track record in production.

**ArgoCD integration depth**: If you are all-in on ArgoCD, Kargo's `argocd-update`
promotion step and ArgoCD-native health checks are deeply integrated. kardinal's ArgoCD
adapter covers health verification but the update mechanism is GitOps-native (git commits).

---

## Where GitOps Promoter Leads

**Git-native purity**: GitOps Promoter has no state outside Git. Every promotion is
a branch, a commit, a PR. No additional CRDs tracking state. If your team wants
"the only source of truth is git," GitOps Promoter's model is the cleanest.

**CommitStatus extensibility**: Any external system can participate in promotion gating
by writing a `CommitStatus` CRD. The open interface is simpler to extend than
kardinal's PolicyGate CEL (which requires running code in-cluster).

---

## When to Choose kardinal-promoter

- You need **parallel environment promotions** — fan-out to prod-us and prod-eu simultaneously, gate on both completing
- You want **expressive, cross-stage policy gates** — soak time, upstream metrics, schedule, bundle metadata — without writing webhook servers
- You use **ArgoCD + Flux mixed**, or neither — kardinal doesn't require a specific GitOps engine
- You want **structured PR evidence** so reviewers have full promotion context in the PR body
- You want **auto-rollback** triggered by health check failures, not just manual revert
- You are a **platform team** that needs org-level policies automatically applied to all pipelines without teams being able to bypass them
- You want **deployment metrics** — time-to-production, rollback rate, operator interventions — surfaced per pipeline

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](faq.md) — common questions
