# Design: Graph Purity — Architecture Tech Debt Tracker

> Status: Active — every logic leak is tracked here
> Related: `docs/design/10-graph-first-architecture.md`
> Last audited: 2026-04-11

---

## The Vision

In a perfectly pure architecture, kardinal-promoter is **pure YAML** from the user's perspective. No custom Go logic exists except:

1. **Owned node reconcilers** that compute a value and write it to `status.ready` — visible to the Graph
2. **CEL library extensions** on krocodile's Graph environment — stateless, synchronous, pure functions
3. **CLI** that reads CRDs and creates CRDs — no business logic, no API calls

Everything else is the Graph. The Graph handles sequencing, fan-out, fan-in, conditional inclusion, and teardown. All business rules are expressed as `readyWhen` / `propagateWhen` / `includeWhen` CEL expressions on Graph nodes.

**The world is a DAG. Everything is a Graph node.**

---

## One Permitted Exception (Transitional)

`pkg/cel/` is a documented transitional workaround. See `docs/design/10-graph-first-architecture.md` §Known Exceptions. It must not grow.

---

## Logic Leak Catalog

Every item below is a place where business logic lives outside the Graph layer. Each has a GitHub issue tracking its elimination. The table is the authoritative index — the issues contain implementation details.

### CRITICAL — Eliminate before v1.0

| ID | Package | Description | Category | GitHub Issue |
|---|---|---|---|---|
| CEL-1 | `pkg/cel/environment.go` | Parallel CEL environment duplicates Graph CEL variables (`bundle`, `environment`, `schedule`) | CEL_DUPLICATE | #130 |
| CEL-2 | `pkg/cel/` + `policygate/reconciler.go` | `buildMetricsContext()` aggregates MetricCheck CRDs in Go before CEL — Graph never sees this aggregation | CEL_DUPLICATE + RECONCILER_DECISION | #131 |
| PG-1 | `policygate/reconciler.go:162` | `time.Now()` + weekday/hour computation inside reconciler — time-based gate logic in Go | TIME | #132 (krocodile recheckAfter) |
| PG-2 | `policygate/reconciler.go:147` | `buildMetricsContext()` cross-CRD aggregation in Go | RECONCILER_DECISION | #131 |
| PS-1 | `promotionstep/reconciler.go:183` | `DefaultSequenceForBundle()` step sequencing in Go — the step sequence is a DAG-within-a-DAG invisible to the Graph | SEQUENCING | #132 |
| PS-4 | `promotionstep/reconciler.go:348` | `r.SCM.GetPRStatus()` live GitHub API call on reconcile hot path | EXTERNAL_API | #133 |
| PS-6 | `promotionstep/reconciler.go:495` | Auto-rollback threshold comparison in Go — decision invisible to Graph | RECONCILER_DECISION | #134 |
| PS-7 | `promotionstep/reconciler.go:737` | `maybeCreateAutoRollback()` creates Bundle CRD from PromotionStep reconciler — Graph-bypassing side effect | RECONCILER_DECISION | #134 |
| ST-1 | `pkg/steps/defaults.go:44` | `DefaultSequenceForBundle()` step routing algorithm in Go | SEQUENCING | #132 |
| ST-2 | `pkg/steps/engine.go:46` | `Engine.ExecuteFrom()` is a mini-scheduler invisible to the Graph | SEQUENCING | #132 |
| ST-3 | `pkg/steps/custom.go:91` | `CustomWebhookStep.Execute()` makes live HTTP POST with blocking retries inside reconciler | EXTERNAL_API | #135 |
| ST-4 | `pkg/steps/custom.go:130` | `time.After(retryBackoff)` blocking sleep inside reconcile loop | TIME + EXTERNAL_API | #135 |
| HE-1 | `pkg/health/adapter.go:139` | `DeploymentAdapter` reads Deployment — should be a Watch node | RESOURCE_ATTR | #136 |
| HE-2 | `pkg/health/adapter.go:180` | `ArgoCDAdapter` reads Application — should be a Watch node | RESOURCE_ATTR | #136 |
| HE-3 | `pkg/health/adapter.go:238` | `FluxAdapter` reads Kustomization — should be a Watch node | RESOURCE_ATTR | #136 |
| SCM-2 | `pkg/scm/github.go:97` | `GetPRStatus()` decision should be a `PRStatus` CRD with Watch node | EXTERNAL_API | #133 |
| CLI-1 | `cmd/kardinal/policy.go:182` | CLI imports `pkg/cel` — banned outside `pkg/reconciler/policygate` | CEL_DUPLICATE | #137 |
| CLI-2 | `cmd/kardinal/policy.go:174` | CLI computes `schedule.isWeekend` client-side, duplicating PolicyGate reconciler logic | TIME + CEL_DUPLICATE | #137 |

### HIGH — Eliminate before Workshop 2

| ID | Package | Description | Category | GitHub Issue |
|---|---|---|---|---|
| PG-3 | `policygate/reconciler.go:197` | `buildUpstreamContext()` computes `soakMinutes` via `time.Since()` in Go | TIME + RESOURCE_ATTR | #133 |
| PG-4 | `policygate/reconciler.go:30` | `defaultRecheckInterval` timer loop in Go — documented workaround for missing kro `recheckAfter` | TIME | #134 (recheckAfter upstream) |
| PS-2 | `promotionstep/reconciler.go:131` | `Pipeline.Spec.Paused` check in Go — should be `includeWhen` on Graph node | RESOURCE_ATTR | #135 |
| PS-5 | `promotionstep/reconciler.go:388` | Health check timeout computed via `time.Since()` in Go | TIME | #136 |
| PS-9 | `promotionstep/reconciler.go:607` | `copyEvidenceToBundle()` writes to Bundle.status from PromotionStep reconciler — cross-CRD mutation | RECONCILER_DECISION | #137 |
| PS-10 | `promotionstep/reconciler.go:262` | Step outputs (`StepState.Outputs`) in-memory map — dependencies between steps invisible to Graph | SEQUENCING | #127 |
| HE-4 | `pkg/health/adapter.go:314` | `AutoDetector.Select()` probes CRDs at runtime to choose adapter — should be Pipeline spec field | RECONCILER_DECISION | #138 |
| ST-5 | `pkg/steps/kustomize.go:55` | `exec.CommandContext("kustomize")` — external binary call in reconcile path | EXTERNAL_API | #139 |
| ST-6 | `pkg/steps/kustomize_build.go:51` | `exec.CommandContext("kustomize build")` + `os.WriteFile` — host-local state | EXTERNAL_API | #139 |
| ST-7 | `pkg/steps/git_clone.go:50` | `gitClient.Clone()` blocking network call + host-local filesystem state | EXTERNAL_API | #140 |
| ST-8 | `pkg/steps/git_commit.go:51` | `gitClient.CommitAll()` host-local git operation | EXTERNAL_API | #140 |
| ST-9 | `pkg/steps/git_push.go:41` | `gitClient.Push()` token injection via `git remote set-url` host mutation | EXTERNAL_API | #140 |
| ST-10 | `pkg/steps/open_pr.go:72` | `state.SCM.OpenPR()` GitHub API call in step engine | EXTERNAL_API | #128 |
| ST-11 | `pkg/steps/wait_for_merge.go:52` | `state.SCM.GetPRStatus()` duplicated in step engine (also exists in reconciler PS-4) | EXTERNAL_API | #128 |
| ST-12 | `pkg/steps/step.go:68` | `StepState` in-memory context blob — step dependencies not in CRD fields | SEQUENCING | #127 |
| SCM-5 | `pkg/scm/git_client.go:84` | `Push()` mutates `git remote set-url` on controller host — mutable state with security implication | EXTERNAL_API | #140 |
| GB-2 | `pkg/graph/builder.go:267` | `validateSkipPermissions()` evaluates gates in Go at build time — Graph never sees this check | RECONCILER_DECISION | #141 |
| BU-1 | `bundle/reconciler.go:120` | `supersedeSiblings()` business rule (one active promotion per pipeline) in Go loop | RECONCILER_DECISION | #142 |
| BU-2 | `bundle/reconciler.go:186` | `Pipeline.Spec.Paused` check duplicated in Bundle reconciler | RESOURCE_ATTR | #135 |
| BU-3 | `bundle/reconciler.go:229` | `Start()` calls `SCM.GetPRStatus()` for all in-flight PRs at startup | EXTERNAL_API | #128 |
| WH-1 | `webhook.go:144` | `reconcileMergedPR()` does reconciler work in HTTP handler | RECONCILER_DECISION | #143 |

### MEDIUM — Clean up progressively

| ID | Package | Description | Category | GitHub Issue |
|---|---|---|---|---|
| PG-5 | `policygate/reconciler.go:64` | Template/instance distinction via label check in Go | RECONCILER_DECISION | #144 |
| PG-6 | `policygate/reconciler.go:246` | `extractVersion()` routing logic, result never in CRD status | RECONCILER_DECISION | #144 |
| PS-3 | `promotionstep/reconciler.go:118` | Shard filtering in Go — silent skip invisible to Graph | RECONCILER_DECISION | #145 |
| PS-8 | `promotionstep/reconciler.go:452` | Health adapter naming convention hardcoded in Go | RECONCILER_DECISION | #138 |
| HE-5 | `pkg/health/adapter.go:338` | `crdAvailable()` live API call on every reconcile | EXTERNAL_API | #138 |
| GB-1 | `pkg/graph/builder.go:112` | Sequential ordering default in builder, not in Pipeline spec | RECONCILER_DECISION | #146 |
| GB-5 | `pkg/graph/builder.go:467` | Fan-in positional naming workaround for missing `dependsOn` | SEQUENCING | #134 (dependsOn upstream) |
| TR-1 | `translator.go:99` | `collectGates()` namespace aggregation in Go | RECONCILER_DECISION | #147 |
| CLI-3 | `cmd/kardinal/policy.go:229` | Gate filtering by `applies-to` reimplemented in CLI | RECONCILER_DECISION | #132 |
| CLI-4 | `cmd/kardinal/rollback.go:67` | "Latest Verified bundle" query in CLI Go loop | RECONCILER_DECISION | #148 |
| CLI-5 | `cmd/kardinal/rollback.go:93` | Hardcoded `Type: "image"` for rollback bundle | RECONCILER_DECISION | #148 |
| BU-4 | `bundle/reconciler.go:138` | Type-aware supersession rule in Go | RECONCILER_DECISION | #142 |
| WH-2 | `webhook.go:165` | URL parsing logic triplicated across three files | RECONCILER_DECISION | #143 |

### LOW — When time permits

| ID | Package | Description | Category | GitHub Issue |
|---|---|---|---|---|
| SCM-3 | `pkg/scm/github.go:183` | `EnsureLabels()` repo config side-effect in promotion path | EXTERNAL_API | #149 |
| SCM-4 | `pkg/scm/pr_template.go:50` | `time.Since()` in PR body template | TIME | #149 |
| GB-3 | `builder.go:438` | `defaultStepType()` routing — acceptable (written to CRD spec) | RECONCILER_DECISION | #146 |
| GB-4 | `builder.go:357` | Dual slug functions (CEL-safe vs K8s-safe) | RECONCILER_DECISION | #146 |
| TR-2 | `translator.go:36` | `policyNS` default hardcoded | RECONCILER_DECISION | #147 |
| CLI-6 | `init.go:50` | `approvalModeFunc()` last-env default in scaffold | RECONCILER_DECISION | — (acceptable for scaffold) |
| CLI-7 | `explain.go:139` | Three-way gate state derived in CLI | RECONCILER_DECISION | #150 |
| MC-1 | `metriccheck/reconciler.go:127` | Threshold comparison Go enum — could be CEL expression | CEL_DUPLICATE | #150 |

---

## Elimination Paths by Mechanism

### Mechanism A: Watch Nodes (highest ROI — no new CRDs needed)

Replace Go adapters with Graph Watch nodes reading existing K8s resources directly:

```
HE-1: Deployment → readyWhen: ${deployment.status.conditions[?type=='Available'].status == 'True'}
HE-2: ArgoCD Application → readyWhen: ${app.status.health.status == 'Healthy' && app.status.sync.status == 'Synced'}
HE-3: Flux Kustomization → readyWhen: ${ks.status.conditions[?type=='Ready'].status == 'True'}
PS-2/BU-2: Pipeline.Spec.Paused → includeWhen: ${pipeline.spec.paused == false} on all nodes
```

### Mechanism B: New CRDs as Graph-observable intermediaries

| New CRD | Replaces | Reconciler writes | Graph reads via |
|---|---|---|---|
| `PRStatus` | PS-4, SCM-2, ST-10, ST-11, BU-3, WH-1 | `status.merged`, `status.open`, `status.prURL` | Watch node `readyWhen: ${prStatus.status.merged}` |
| `SoakTimer` | PG-3 | `status.soakMinutes` | Watch node in PolicyGate context |
| `RollbackPolicy` | PS-6, PS-7 | `status.shouldRollback` | Watch node triggering Bundle creation |

### Mechanism C: CEL library extensions on kro (requires upstream contribution)

```
schedule.isWeekend() → eliminates PG-1, CLI-2
schedule.hour()
schedule.dayOfWeek()
Requires: recheckAfter kro primitive first (issue #134)
```

### Mechanism D: Step-as-Graph-node refactor (largest, requires architecture decision)

Each promotion step becomes a dedicated Graph node:
```
git-clone     → Graph node (Owned, writes status.cloned)
set-image     → Graph node (Owned, writes status.imageSet)
git-commit    → Graph node (Owned, writes status.committed)
git-push      → Graph node (Owned, writes status.pushed)
open-pr       → Graph node (Owned, writes status.prURL)
wait-for-merge → Watch node on PRStatus CRD
health-check  → Watch node on Deployment/Application/Kustomization
```
Eliminates: PS-1, PS-10, ST-1, ST-2, ST-5–ST-12

Requires human architectural decision: should each step be a separate CRD type, or a generic `Step` CRD with a `type` field?

---

## Pending Upstream Contributions to krocodile

| Contribution | Eliminates | Tracked |
|---|---|---|
| `recheckAfter` on Graph nodes | PG-4, GB-5 | #134 |
| Explicit `dependsOn` edges | GB-5 | #134 |
| `ShapeWatch` for external K8s resources | HE-1, HE-2, HE-3 | #131 |
| CEL schedule library | PG-1, CLI-2 | #126 |
| Server-side policy simulation API | CLI-1, CLI-2 | #132 |
