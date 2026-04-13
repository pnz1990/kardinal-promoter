# Roadmap

This page describes what is currently available in kardinal-promoter and what is planned for future releases.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub Discussion](https://github.com/pnz1990/kardinal-promoter/discussions) if a feature matters to your use case.

---

## Currently Available (v0.4.0)

All of the following are implemented and shipped:

**Core promotion engine**
- Pipeline CRD with DAG-native stage ordering and fan-out
- Bundle CRD with image and config artifact types
- PolicyGate CRD with CEL expressions (kro library: schedule, soak, metrics, upstream)
- PromotionStep reconciler — full git-clone → kustomize/helm → commit → PR → merge → health loop
- Graph-first architecture via krocodile

**Manifest update strategies**
- `kustomize` — `kustomize edit set-image`
- `helm` — patch values.yaml at configurable path
- `config-merge` — GitOps config-only promotions

**Health adapters**
- `resource` — Kubernetes Deployment condition
- `argocd` — Argo CD Application health + sync
- `flux` — Flux Kustomization Ready
- `argoRollouts` — Argo Rollouts Rollout phase
- `flagger` — Flagger Canary phase

**SCM providers**
- GitHub (webhooks + polling)
- GitLab (webhooks + polling)

**Gates and policies**
- CEL context: schedule, bundle metadata, upstream soak time, metrics
- MetricCheck CRD (PromQL-based metric injection into CEL)
- Org-level gates (mandatory, cannot be bypassed by teams)
- Team-level gates (additive)
- SkipPermission gates

**Operations**
- `RollbackPolicy` CRD + automated rollback PR
- Pause/resume (`Bundle.spec.paused`)
- Supersession for concurrent Bundles
- Multi-cluster via kubeconfig Secrets

**CLI** — full command set: `get`, `explain`, `create`, `rollback`, `approve`, `pause`, `resume`, `history`, `policy`, `diff`, `logs`, `metrics`, `version`

**UI** — embedded React DAG visualization with gate states, bundle timeline, health chips

**Distributed mode** — controller + shard agents (in progress, Stage 14)

---

## Near-Term (v0.5.0 — active development)

### K-01: Contiguous healthy soak

`soakMinutes` today counts total elapsed time since the upstream environment was verified.
This will change to count **contiguous healthy minutes** — if health fails during the soak
window, the timer resets to zero.

New stage-level shorthand:

```yaml
spec:
  environments:
    - name: prod
      bake:
        minutes: 200
        policy: reset-on-alarm   # reset timer if health event occurs
```

Under the hood `bake:` generates the correct `soakMinutes` PolicyGate automatically.
`soakMinutes` in CEL expressions continues to work as-is; users who write it manually
get the new contiguous semantics.

**Why**: The difference between "the service has been running for 60 minutes" and "the
service has been *healthy* for 60 contiguous minutes" is operationally critical.

---

### K-02: Pre-deploy gate type

PolicyGates today are evaluated after a deployment starts (post-deploy). A `when: pre-deploy`
gate is evaluated *before* the deployment begins — if it fails, the PromotionStep stays in
`Waiting` and never opens a PR.

```yaml
kind: PolicyGate
spec:
  when: pre-deploy   # evaluated before git-clone starts
  expression: 'health.deployment("my-app", "staging").errorRate < 0.01'
```

This maps to the "alarm blocker" pattern — health must be green in the upstream environment
before the next deployment even starts.

---

### K-03: Auto-rollback with ABORT vs ROLLBACK distinction

Stage-level `onHealthFailure` policy:

```yaml
spec:
  environments:
    - name: prod
      onHealthFailure: rollback   # vs. abort | none
```

`rollback` — automatically creates a new Bundle at the previous image version when health
fails during the soak window. The rollback Bundle travels the full pipeline with evidence.

`abort` — freezes the deployment in its current state. Requires a human decision before anything
continues. Used when the health failure might be a false positive.

`none` — current behavior (stage fails, downstream stops).

---

### K-04: ChangeWindow CRD

A cluster-scoped `ChangeWindow` resource in the `kardinal-system` namespace. When active,
blocks all pipeline promotions in the cluster automatically — no per-pipeline configuration needed.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeWindow
metadata:
  name: q4-holiday-freeze
  namespace: kardinal-system
spec:
  type: blackout
  start: "2026-11-25T00:00:00Z"
  end:   "2026-11-29T00:00:00Z"
  reason: "Q4 holiday freeze"
---
kind: ChangeWindow
metadata:
  name: business-hours
spec:
  type: recurring
  schedule:
    timezone: America/Los_Angeles
    allowedDays: [Mon, Tue, Wed, Thu]
    allowedHours: "09:00-17:00"
```

CEL:
```
changewindow.isAllowed("business-hours") && !changewindow.isBlocked("q4-holiday-freeze")
```

**Why**: Platform teams need a single lever to freeze all pipelines during incidents,
releases, or planned maintenance. Today this requires updating every Pipeline or every
PolicyGate manually.

---

### K-05: Deployment metrics

Bundle and Pipeline status will include deployment efficiency metrics:

```yaml
# Bundle.status.metrics
metrics:
  commitToFirstStageMinutes: 12
  commitToProductionMinutes: 847
  bakeResets: 1            # how many times health reset the soak timer
  operatorInterventions: 0 # how many gates were manually overridden

# Pipeline.status.deploymentMetrics (aggregate over last 30 Bundles)
deploymentMetrics:
  rolloutsLast30Days: 12
  p50CommitToProdMinutes: 420
  p90CommitToProdMinutes: 960
  autoRollbackRate: 0.08
  operatorInterventionRate: 0.0
  staleProdDays: 3
```

Surfaced in the UI as a trend chart on the pipeline view. High P90 = something is regularly
blocking deployments. High rollback rate = health checks are catching real problems (good)
or are too sensitive (needs tuning).

---

## Planned (v0.6.0+)

### K-06: Wave topology

Syntactic sugar over DAG `dependsOn` for multi-region production rollouts:

```yaml
spec:
  environments:
    - name: prod-wave-1
      wave: 1
      bake: { minutes: 720 }
    - name: prod-wave-2
      wave: 2
      bake: { minutes: 200 }
    - name: prod-wave-3
      wave: 3
      bake: { minutes: 200 }
```

Wave N stages automatically depend on all Wave N-1 stages. The Pipeline translator
generates the DAG edges. This is pure syntactic sugar — it generates the same graph
users can write manually with `dependsOn`.

---

### K-07: Integration test step

A built-in step type that runs a Kubernetes Job as part of the promotion:

```yaml
steps:
  - uses: integration-test
    config:
      image: ghcr.io/myorg/integration-tests:latest
      command: ["./run-tests.sh", "--env", "staging"]
      timeout: 30m
      onFailure: abort   # vs. rollback
```

The step creates a Job, watches completion, and writes the result to PromotionStep status.
On failure, triggers the `onFailure` policy.

---

### K-08: PR review gate

Check that the PR for a given stage has been reviewed and approved before advancing:

```yaml
kind: PolicyGate
spec:
  when: pre-deploy
  expression: 'bundle.pr("staging").isApproved() && bundle.pr("staging").hasMinReviewers(1)'
```

Reads existing `PRStatus` CRD. No new infrastructure.

---

### K-09: `kardinal override` with audit record

Emergency escape hatch for production incidents:

```bash
kardinal override my-app --stage prod --gate no-weekend-deploy \
  --reason "P0 hotfix — incident #4521"
```

Patches the PolicyGate with a time-limited override and writes a mandatory audit record
to Bundle status:

```yaml
status:
  gateOverrides:
    - gate: no-weekend-deploy
      stage: prod
      reason: "P0 hotfix — incident #4521"
      at: "2026-04-13T02:34:00Z"
```

The override record appears in the PR evidence body.

---

### K-10: Subscription CRD (passive Bundle creation)

!!! warning "Under design"
    The Subscription CRD is under design. The spec below is illustrative.

Watches external sources and auto-creates Bundles without CI modification:

```yaml
kind: Subscription
spec:
  pipeline: my-app
  source:
    type: image
    image:
      repository: ghcr.io/myorg/my-app
      constraint: ">=1.0.0"
      interval: 5m
```

---

### K-11: Cross-stage history CEL functions

New CEL functions that query pipeline history within a gate expression:

```
upstream.staging.lastNPromotionsSucceeded(3)   # last 3 staging promotions all passed health
upstream.staging.healthContiguousMinutes        # contiguous healthy minutes (live)
```

These deepen the Graph-first moat — gates can encode organizational deployment wisdom
as code that competitors cannot express.

---

## Not Planned

These features are out of scope for kardinal. Use the recommended tools instead:

| Feature | Recommended tool |
|---|---|
| Passive OCI artifact discovery | Kargo Warehouses, Flux ImagePolicy |
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
