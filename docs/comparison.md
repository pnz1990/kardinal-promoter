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
| **PR evidence body** | Structured (image, digest, CI run, commit and author; each gate's result and reason; upstream health-check times) | None — tracked in Kargo UI | Git diff only |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes | ArgoCD (primary), others partial | ArgoCD, Flux, any |
| **SCM providers** | GitHub, GitLab, Forgejo/Gitea; Bitbucket Cloud and Azure DevOps (experimental, one provider per controller) | GitHub, GitLab | GitHub |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts, Flagger | ArgoCD Application | ArgoCD Application |
| **Rollback mechanism** | Promotion of previous artifact through same pipeline | Manual | Manual git revert |
| **Auto-rollback on health failure** | Yes — `onHealthFailure: rollback \| abort \| none` per stage | No | No |
| **Contiguous healthy soak** | Yes — `bake.minutes` resets timer on health alarm | No — elapsed time only | No — elapsed time only |
| **Change freeze management** | Yes — cluster-scoped `ChangeWindow` (blackout or recurring), enforced by any gate that references it | No | Manual CommitStatus |
| **Wave topology** | Yes — `wave:` field generates multi-region DAG edges automatically | No | No |
| **CLI** | Full `kardinal` CLI incl. `override`, `metrics`, `logs`, `validate`, `status`, shell completion | `kargo` CLI | No CLI |
| **UI dashboard** | Embedded UI: fleet health bar, ops table, pipeline lane and DAG, bundle timeline and comparison, policy gates with CEL expressions, metrics bar; create bundle, pause/resume, promote and roll back from the UI (approve and gate override are CLI-only) | Polished Kargo UI | No UI |
| **Metric-gated promotions** | Yes (`MetricCheck` CRD + PromQL) | No | No |
| **DORA metrics** | Yes — `Bundle.status.metrics`, `kardinal metrics` CLI | No | No |
| **Integration test step** | Not yet — the `integration-test` step is built, but a Pipeline cannot select it until `spec.environments[].steps` is implemented | No | No |
| **Image signature verification** | Not yet — the `verify-image` step (cosign) is built, but a Pipeline cannot select it until `spec.environments[].steps` is implemented | No | No |
| **Emergency gate override** | Yes — `kardinal override` with mandatory reason + audit record | No | No |
| **Outbound event notifications** | Yes — `NotificationHook` CRD fires HTTP webhooks on Bundle.Verified, PolicyGate.Blocked, PromotionStep.Failed; optional auth header; pipeline selector | Yes (Kargo via Argo Notifications) | No |
| **Multi-cluster** | Argo CD hub-spoke (health read from hub Applications); `health.cluster` kubeconfig Secrets not implemented | Yes | Yes |
| **Upstream soak time in gates** | Yes — `bundle.upstreamSoakMinutes >= 30` (contiguous healthy) | No | Elapsed time only |
| **Cross-stage history in gates** | Yes — `upstream.<env>.recentSuccessCount`, `lastPromotedAt` | No | No |
| **Artifact discovery** | Bundle created by CI/CLI; Subscription CRD with OCI + Git watchers | Warehouse (automatic OCI/git scanning) | Git commit-based |
| **Multi-artifact bundle** | Yes (image + config in one Bundle) | Yes (Freight) | No |
| **Architecture** | Graph-first (kro Graph DAG) | Stage/controller | Controller |
| **Maturity** | v0.8.1, active development | v1.10.x, production-grade | v0.27.x, experimental |
| **License** | Apache 2.0 | Apache 2.0 | Apache 2.0 |

---

## Why kardinal-promoter

### Graph-native policy evaluation

kardinal PolicyGates are nodes in the kro DAG. They have access to the entire pipeline's
state — not just the current stage:

```yaml
# In a prod PolicyGate — reads upstream uat stage's soak time
expression: "bundle.upstreamSoakMinutes >= 60"

# Read a MetricCheck result
expression: 'metrics["error-rate"].result == "Pass"'

# Combine schedule + soak + metadata
expression: '!schedule.isWeekend && bundle.upstreamSoakMinutes >= 30 && !(has(bundle.labels.hotfix) && bundle.labels.hotfix == "true")'
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

Every promotion PR opened by kardinal has a body like this (`pkg/scm/pr_template.go`; see
[PR Evidence](pr-evidence.md)):

```markdown
## Promotion: my-app-x7k2p -> my-app/prod

### Artifact Provenance

| Image | Tag | Digest | CI Run | Commit SHA | Author |
|---|---|---|---|---|---|
| ghcr.io/myorg/my-app | 1.29.0 | sha256:a1b2c3d4 | [CI run](https://github.com/myorg/my-app/actions/runs/12345) | abc123d | engineer-name |

### Policy Gate Compliance

| Gate | Namespace | Result | Reason | Last Evaluated |
|---|---|---|---|---|
| no-weekend-deploys | my-app | Pass | bundle.version=1.29.0: !schedule.isWeekend = true | 2026-04-14T14:00Z |

### Upstream Verification

| Environment | Health Checked At | Elapsed |
|---|---|---|
| staging | 2026-04-14T13:15Z | 45m |
```

Kargo tracks promotions in its own UI — PRs have no evidence body. GitOps Promoter
PRs show the git diff only.

### Auto-rollback and health-failure policy

With `onHealthFailure: rollback` (the default is `none`), kardinal creates a rollback Bundle
when a promotion fails health verification. Each stage can independently configure `onHealthFailure: rollback | abort | none`.
Combined with `bake.policy: fail-on-alarm`, this gives fine-grained control: critical stages
abort and require human intervention; non-critical stages roll back automatically.
Neither competitor has automated rollback or stage-level health failure policies.

### Contiguous healthy soak

`bake.minutes` counts *contiguous* healthy minutes — if a health alarm fires during the
soak window, the timer resets to zero. The deployment must survive a full `bake.minutes`
window with no alarms. Kargo and GitOps Promoter both count elapsed time from deployment,
regardless of whether the service was healthy during that window.

### Change freeze management

A `ChangeWindow` is a cluster-scoped object: a one-off blackout (`start`/`end`) or a
recurring allowed window (days, hours, timezone). Platform teams reference it from an
org-level PolicyGate (`!changewindow.isBlocked("freeze")`), so one object blocks every
pipeline during incidents, holidays, or maintenance windows, with no per-pipeline changes. Kargo has no equivalent. GitOps Promoter requires
manually setting CommitStatus resources per-environment.

### Wave topology for multi-region rollouts

The `wave:` field on Pipeline environments generates DAG dependency edges automatically:
wave 2 cannot start until all wave 1 stages are verified. This makes the prod-wave-1 →
prod-wave-2 → prod-wave-3 pattern idiomatic in three lines of YAML. Kargo has no wave
concept. GitOps Promoter has no DAG support.

### DORA metrics built-in

`Bundle.status.metrics` records `commitToProductionMinutes`, `bakeResets` and `operatorInterventions`
(the `kardinal override` entries on the Bundle's gates) for every promotion. The `kardinal metrics` CLI surfaces these
per pipeline. Neither Kargo nor GitOps Promoter tracks deployment efficiency metrics.

---

## Known limitations

kardinal-promoter is the right tool for most of what is described above. There are cases
where a different approach may be a better fit — not because the competition is better,
but because the use case doesn't match what kardinal is designed for.

**You want native ArgoCD updates with a PR review.**
`update.strategy: argocd` patches the Argo CD Application directly, with no git commit (the
equivalent of Kargo's `argocd-update`), but it supports only `approval: auto`: an
environment with `approval: pr-review` fails. See
[ArgoCD-Native Promotion](argocd-native-promotion.md).

**You want zero state outside Git.**
kardinal maintains state in Kubernetes CRDs (Pipeline, Bundle, PromotionStep, AuditEvent).
If your constraint is that every promotion artefact must be a git commit with no
Kubernetes-side state, kardinal is not the right model.

**You need a larger community and commercial support today.**
kardinal-promoter is at v0.8.1 with active development. Kargo has a longer production
track record and commercial backing from Akuity. If your organisation requires vendor
support or a larger existing community before adopting, that is a legitimate constraint.

**Your only gate requirement is "a human clicks approve."**
kardinal's DAG, CEL gates, and structured evidence add meaningful complexity. If your
promotion workflow is simply "CI passes, human approves PR," that complexity is overhead
you don't need.

---

## When to choose kardinal-promoter

- You need **parallel environment promotions** — fan-out to prod-us and prod-eu simultaneously, gate on both completing
- You want **expressive, cross-stage policy gates** — soak time, upstream metrics, schedule, bundle metadata, PR approval state — without writing webhook servers
- You need **contiguous healthy soak** — deployments must survive bake windows with zero health alarms, not just elapsed time
- You want **wave topology** for multi-region production rollouts — promote to 1 region, bake, then expand to the next wave
- You want a **centralized change freeze** — one `ChangeWindow` object, referenced by an org-level gate, blocks all pipelines during incidents or holidays
- You use **ArgoCD + Flux mixed**, or neither — kardinal doesn't require a specific GitOps engine
- You want **structured PR evidence** so reviewers have full promotion context in the PR body
- You want **auto-rollback** triggered by health check failures, with per-stage abort vs. rollback vs. ignore policy
- You are a **platform team** that needs org-level policies automatically applied to all pipelines, which a team Pipeline cannot remove (break-glass overrides are time-limited and recorded, and need `patch` on PolicyGates in the Pipeline namespace)
- You want **DORA metrics** — time-to-production, rollback rate, operator interventions — surfaced per pipeline
- You need **emergency override with audit record** — escape hatch that produces evidence, not a silent bypass

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](faq.md) — common questions
