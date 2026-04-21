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
| **Parallel environments** | Yes — native fan-out + `wave:` topology | No | No — DAG on roadmap |
| **Policy gates** | CEL (kro library: schedule, upstream soak, metrics, cross-stage, PR review) | Manual approval only | CommitStatus-based webhook checks |
| **Cross-stage policy** | Yes — gate can read upstream soak, history, metrics, PR approval state | No | No |
| **Pre-deploy gates** | Yes — `when: pre-deploy` blocks before git-clone starts | No | No |
| **PR evidence body** | Structured (image digest, CI run, gate results, soak time, overrides) | None — tracked in Kargo UI | Git diff only |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes | ArgoCD (primary), others partial | ArgoCD, Flux, any |
| **SCM providers** | GitHub, GitLab, Forgejo/Gitea | GitHub, GitLab | GitHub |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts, Flagger | ArgoCD Application | ArgoCD Application |
| **Rollback mechanism** | Promotion of previous artifact through same pipeline | Manual | Manual git revert |
| **Auto-rollback on health failure** | Yes — `onHealthFailure: rollback \| abort \| none` per stage | No | No |
| **Contiguous healthy soak** | Yes — `bake.minutes` resets timer on health alarm | No — elapsed time only | No — elapsed time only |
| **Change freeze management** | Yes — `ChangeWindow` CRD blocks all pipelines cluster-wide | No | Manual CommitStatus |
| **Wave topology** | Yes — `wave:` field generates multi-region DAG edges automatically | No | No |
| **CLI** | Full `kardinal` CLI incl. `override`, `metrics`, `logs`, `validate`, `status`, shell completion | `kargo` CLI | No CLI |
| **UI dashboard** | Full control plane UI: fleet dashboard, ops view, bake countdown, gate detail panel, bundle timeline, metrics bar, in-UI approve/rollback/override | Polished Kargo UI | No UI |
| **Metric-gated promotions** | Yes (`MetricCheck` CRD + PromQL) | No | No |
| **DORA metrics** | Yes — `Bundle.status.metrics`, `kardinal metrics` CLI | No | No |
| **Integration test step** | Yes — `integration-test` step runs a Kubernetes Job | No | No |
| **Emergency gate override** | Yes — `kardinal override` with mandatory reason + audit record | No | No |
| **Multi-cluster** | Yes (Pipeline CRD, kubeconfig Secrets) | Yes | Yes |
| **Upstream soak time in gates** | Yes — `bundle.upstreamSoakMinutes >= 30` (contiguous healthy) | No | Elapsed time only |
| **Cross-stage history in gates** | Yes — `upstream.<env>.recentSuccessCount`, `lastPromotedAt` | No | No |
| **Artifact discovery** | Bundle created by CI/CLI; Subscription CRD with OCI + Git watchers | Warehouse (automatic OCI/git scanning) | Git commit-based |
| **Multi-artifact bundle** | Yes (image + config in one Bundle) | Yes (Freight) | No |
| **Architecture** | Graph-first (krocodile DAG) | Stage/controller | Controller |
| **Maturity** | v0.8.1, active development | v1.10.x, production-grade | v0.27.x, experimental |
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

### Auto-rollback and health-failure policy

kardinal opens a rollback PR automatically when a promotion fails health verification,
using the previous Bundle. Each stage can independently configure `onHealthFailure: rollback | abort | none`.
Combined with `bake.policy: fail-on-alarm`, this gives fine-grained control: critical stages
abort and require human intervention; non-critical stages roll back automatically.
Neither competitor has automated rollback or stage-level health failure policies.

### Contiguous healthy soak

`bake.minutes` counts *contiguous* healthy minutes — if a health alarm fires during the
soak window, the timer resets to zero. The deployment must survive a full `bake.minutes`
window with no alarms. Kargo and GitOps Promoter both count elapsed time from deployment,
regardless of whether the service was healthy during that window.

### Change freeze management

A single `ChangeWindow` CRD in `kardinal-system` blocks all pipelines cluster-wide.
Platform teams create one object during incidents, holidays, or maintenance windows — no
per-pipeline configuration needed. Kargo has no equivalent. GitOps Promoter requires
manually setting CommitStatus resources per-environment.

### Wave topology for multi-region rollouts

The `wave:` field on Pipeline environments generates DAG dependency edges automatically:
wave 2 cannot start until all wave 1 stages are verified. This makes the prod-wave-1 →
prod-wave-2 → prod-wave-3 pattern idiomatic in three lines of YAML. Kargo has no wave
concept. GitOps Promoter has no DAG support.

### DORA metrics built-in

`Bundle.status.metrics` records `commitToProductionMinutes`, `bakeResets`, and
`operatorInterventions` for every promotion. The `kardinal metrics` CLI surfaces these
per pipeline. Neither Kargo nor GitOps Promoter tracks deployment efficiency metrics.

---

## Where Kargo Leads

**Artifact discovery**: Kargo's Warehouse concept automatically monitors OCI registries
and Git repos for new artifact versions. kardinal now has equivalent capability via the
**Subscription CRD** — an OCI watcher polls a registry for new tags and creates Bundles
automatically; a Git watcher monitors a repository for config changes. This gap is closed
as of v0.6.0. Kargo's Warehouse UI has a polished exploration interface that kardinal's
Subscription does not match yet.

**Production maturity**: Kargo is at v1.10.x with commercial support from Akuity,
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
- You want **expressive, cross-stage policy gates** — soak time, upstream metrics, schedule, bundle metadata, PR approval state — without writing webhook servers
- You need **contiguous healthy soak** — deployments must survive bake windows with zero health alarms, not just elapsed time
- You want **wave topology** for multi-region production rollouts — promote to 1 region, bake, then expand to the next wave
- You want a **centralized change freeze** — one `ChangeWindow` object blocks all pipelines during incidents or holidays
- You use **ArgoCD + Flux mixed**, or neither — kardinal doesn't require a specific GitOps engine
- You want **structured PR evidence** so reviewers have full promotion context in the PR body
- You want **auto-rollback** triggered by health check failures, with per-stage abort vs. rollback vs. ignore policy
- You are a **platform team** that needs org-level policies automatically applied to all pipelines without teams being able to bypass them
- You want **DORA metrics** — time-to-production, rollback rate, operator interventions — surfaced per pipeline
- You need **integration tests as promotion steps** — run a Kubernetes Job as part of the promotion sequence
- You need **emergency override with audit record** — escape hatch that produces evidence, not a silent bypass

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](faq.md) — common questions
