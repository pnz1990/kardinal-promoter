# Comparison: kardinal vs Kargo vs GitOps Promoter

This page compares kardinal-promoter with the two most similar tools in the GitOps promotion space.

!!! note "Objectivity"
    This comparison is based on publicly available documentation and source code as of October 2026.
    All three tools are actively developed. Check each project's releases for the latest capabilities.

---

## Feature Matrix

| Feature | kardinal-promoter | Kargo | GitOps Promoter |
|---|---|---|---|
| **Promotion model** | DAG (fan-out, arbitrary dependencies) | DAG of Stages (a Stage takes Freight from one or more upstream Stages) | Environment list, or a DAG with `dependsOn` |
| **Parallel environments** | Yes — native fan-out + `wave:` topology | Yes — several Stages with the same upstream | Yes — `dependsOn` |
| **Policy gates** | CEL with pipeline context: schedule, upstream soak and history, metrics, change windows, PR review state, Bundle metadata | Manual approval, verification (AnalysisTemplates), `requiredSoakTime`; promotion windows in Kargo Enterprise | CommitStatus gates: Argo CD health, soak time, schedule windows, `expr` checks on commits and web requests |
| **Cross-stage policy** | Yes — gate can read upstream soak, history, metrics, PR approval state | Partial — Freight must be verified (and soaked) in the upstream Stage | Partial — `DependentsSuccessfulCommitStatus` waits for upstream success |
| **Pre-deploy gates** | Yes — every gate is re-checked, on a result newer than the step, right before git-clone starts | Upstream verification only; promotion windows in Kargo Enterprise | Yes — proposed commit statuses must pass before the PR merges |
| **PR evidence body** | Structured (image, digest, CI run, commit and author; each gate's result and reason; upstream health-check times) | A description you write in the `git-open-pr` step; no built-in evidence | Templated: deployed and proposed commits, a diff link, the environment table |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes | ArgoCD (primary), others partial | ArgoCD, Flux, any |
| **SCM providers** | GitHub, GitLab, Forgejo/Gitea; Bitbucket Cloud and Azure DevOps (experimental, one provider per controller) | GitHub, GitLab, Gitea, Bitbucket, Azure DevOps | GitHub, GitHub Enterprise, GitLab, Forgejo, Gitea, Bitbucket Cloud, Azure DevOps |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts, Flagger | ArgoCD Application | ArgoCD Application |
| **Rollback mechanism** | Promotion of previous artifact through same pipeline | Manual | Manual git revert |
| **Auto-rollback on health failure** | Yes — `onHealthFailure: rollback \| abort \| none` per stage | Kargo Enterprise only (beta) | No |
| **Contiguous healthy soak** | Yes — `bake.minutes` resets timer on health alarm | No — elapsed time only | No — elapsed time only |
| **Change freeze management** | Yes — cluster-scoped `ChangeWindow` (blackout or recurring), enforced by any gate that references it | Kargo Enterprise only (promotion windows, v1.12+) | `ScheduledCommitStatus` cron windows, per PromotionStrategy |
| **Wave topology** | Yes — `wave:` field generates multi-region DAG edges automatically | No | No |
| **CLI** | Full `kardinal` CLI incl. `override`, `metrics`, `logs`, `validate`, `status`, shell completion | `kargo` CLI | No CLI |
| **UI dashboard** | Embedded UI: fleet health bar, ops table, pipeline lane and DAG, bundle timeline and comparison, policy gates with CEL expressions, metrics bar; create bundle, pause/resume, promote and roll back from the UI (gate override is CLI-only) | Polished Kargo UI | Web dashboard |
| **Metric-gated promotions** | Yes (`MetricCheck` CRD + PromQL) | Yes — verification with AnalysisTemplates (Prometheus, Datadog and others) | Indirect — a `WebRequestCommitStatus` can call a metrics API |
| **DORA metrics** | Yes — `Bundle.status.metrics`, `kardinal metrics` CLI | No | Planned (roadmap) |
| **Custom promotion steps** | No — each environment runs a fixed sequence chosen by the Bundle type, `update.strategy` and `approval` ([Promotion Steps](pipeline-reference.md#promotion-steps)) | Yes — a Stage's `promotionTemplate` lists promotion steps | No |
| **Integration test step** | No — run tests as an Argo CD PostSync hook with `health.type: argocd`, or gate on a `MetricCheck` ([how](pipeline-reference.md#image-signatures-and-tests)) | Yes — verification can run a Kubernetes Job (AnalysisTemplate) | No |
| **Image signature verification** | No — use admission-time verification in the workload cluster (Sigstore policy-controller or Kyverno `verifyImages`; [how](pipeline-reference.md#image-signatures-and-tests)) | No | No |
| **Emergency gate override** | Yes — `kardinal override`: time-limited; writes the mandatory reason, expiry, time and local OS user name to the gate's `spec.overrides`, and the PR evidence shows it (no AuditEvent yet) | Manual Freight approval (`kargo approve`), with no reason or expiry | No |
| **Outbound event notifications** | Yes — `NotificationHook` CRD fires HTTP webhooks on Bundle.Verified, Bundle.Failed, PolicyGate.Blocked, PromotionStep.Failed; optional auth header (plain text in the spec); pipeline selector | Kargo on the Akuity Platform only (`send-message` step, v1.8+) | No |
| **Multi-cluster** | Argo CD or Flux hub (health read from the hub's Applications or Kustomizations); `health.cluster` kubeconfig Secrets are not supported | Yes | Yes |
| **Upstream soak time in gates** | Yes — `bundle.upstreamSoakMinutes >= 30` (minutes since the upstream was Verified) | Yes — `requiredSoakTime` (elapsed time) | Yes — `TimedCommitStatus` (elapsed time) |
| **Cross-stage history in gates** | Yes — `upstream.<env>.recentSuccessCount`, `lastPromotedAt` | No | No |
| **Artifact discovery** | Bundle created by CI/CLI; Subscription CRD with OCI + Git watchers | Warehouse (automatic OCI/git scanning) | Git commit-based |
| **Multi-artifact bundle** | Yes (image + config in one Bundle) | Yes (Freight) | No |
| **Architecture** | Graph-first (kro Graph DAG) | Stage/controller | Controller |
| **Maturity** | v0.9.0-rc.1 (release candidate), active development | v1.12.x, production-grade | v0.42.x, experimental |
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

Kargo (`requiredSoakTime`) and GitOps Promoter (`TimedCommitStatus`) can wait a fixed time
after the upstream promotion. Neither resets the timer on a health alarm, or combines soak
time with schedule, metrics and Bundle metadata in one expression.

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

Kargo's `git-open-pr` step takes a description you write yourself; Kargo adds no evidence.
GitOps Promoter's default PR template lists the deployed and proposed commits, a diff link and
the environment table, with no provenance or gate results.

### Contiguous healthy soak

`bake.minutes` counts *contiguous* healthy minutes — if a health alarm fires during the
soak window, the timer resets to zero. The deployment must survive a full `bake.minutes`
window with no alarms. Kargo and GitOps Promoter both count elapsed time from deployment,
regardless of whether the service was healthy during that window.

### Change freeze management

A `ChangeWindow` is a cluster-scoped object: a one-off blackout (`start`/`end`) or a
recurring allowed window (days, hours, timezone). Platform teams reference it from an
org-level PolicyGate (`!changewindow.isBlocked("freeze")`), so one object blocks every
pipeline during incidents, holidays, or maintenance windows, with no per-pipeline changes. Kargo OSS has no equivalent; Kargo Enterprise has promotion
windows (v1.12+). GitOps Promoter has `ScheduledCommitStatus`, set up per PromotionStrategy.

### Wave topology for multi-region rollouts

The `wave:` field on Pipeline environments generates DAG dependency edges automatically:
wave 2 cannot start until all wave 1 stages are verified. This makes the prod-wave-1 →
prod-wave-2 → prod-wave-3 pattern idiomatic in three lines of YAML. Kargo and GitOps
Promoter have no wave concept.

### DORA metrics built-in

`Bundle.status.metrics` records `commitToProductionMinutes`, `bakeResets` and `operatorInterventions`
(the `kardinal override` entries on the Bundle's gates) for every promotion. The `kardinal metrics` CLI surfaces these
per pipeline. Kargo does not track them; GitOps Promoter lists DORA metrics on its roadmap.

### Auto-rollback and health-failure policy

With `onHealthFailure: rollback` (the default is `none`), kardinal creates a rollback Bundle
when a promotion fails health verification. Each stage can independently configure `onHealthFailure: rollback | abort | none`.
Combined with `bake.policy: fail-on-alarm`, this gives fine-grained control: critical stages
abort and require human intervention; non-critical stages roll back automatically.
GitOps Promoter has no automated rollback. Kargo has it only in Kargo Enterprise (beta).

### Fan-out DAG topology

```
test ──► uat ──► staging ──┬──► prod-us ──┐
                           └──► prod-eu ──┴──► verified
```

All three tools can run this. kardinal models it natively, and `wave:` can generate the edges.
Kargo builds it from Stages that request Freight from upstream Stages. GitOps Promoter builds
it with `dependsOn` on its environments.

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
kardinal-promoter is at v0.9.0-rc.1 with active development. Kargo has a longer production
track record and commercial backing from Akuity. If your organisation requires vendor
support or a larger existing community before adopting, that is a legitimate constraint.

**Your only gate requirement is "a human clicks approve."**
kardinal's DAG, CEL gates, and structured evidence add meaningful complexity. If your
promotion workflow is simply "CI passes, human approves PR," that complexity is overhead
you don't need.

---

## When to choose kardinal-promoter

- You want **expressive, cross-stage policy gates** — soak time, upstream metrics, schedule, bundle metadata, PR approval state — without writing webhook servers
- You need **contiguous healthy soak** — deployments must survive bake windows with zero health alarms, not just elapsed time
- You want **wave topology** for multi-region production rollouts — promote to 1 region, bake, then expand to the next wave
- You want a **centralized change freeze** — one `ChangeWindow` object, referenced by an org-level gate, blocks all pipelines during incidents or holidays
- You use **ArgoCD + Flux mixed**, or neither — kardinal doesn't require a specific GitOps engine
- You want **structured PR evidence** so reviewers have full promotion context in the PR body
- You want **auto-rollback** triggered by health check failures, with per-stage abort vs. rollback vs. ignore policy
- You are a **platform team** that needs org-level policies automatically applied to all pipelines, which a team Pipeline cannot remove (break-glass overrides are time-limited and recorded, and need `patch` on PolicyGates in the Pipeline namespace)
- You want **DORA metrics** — time-to-production, rollback rate, operator interventions — surfaced per pipeline
- You need a **time-limited emergency override** that records its reason on the gate and in the PR evidence, not a silent bypass

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](faq.md) — common questions
