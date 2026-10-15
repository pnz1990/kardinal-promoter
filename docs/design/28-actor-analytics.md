# Design: Analytics by actor, the agent fleet view, and per-Pipeline promotion identity

> Status: Proposed (spec for v0.14.0, AGENTIC-PLAN §5 "Analytics by actor" and "Per-Pipeline
> promotion identity"). Implementation waits until v0.13.0 is tagged, in separate PRs.
> Created: 2026-10-15
> Depends on: [17-actors.md](17-actors.md) (the `Actor` stamp on Bundles, `AgentIdentity`,
> `pkg/actor`; epic #1834, implementation PR #1857, not on main yet) and
> [19-verdicts.md](19-verdicts.md) §8.4 (the SubjectAccessReview bounds pattern for a Pod
> identity). It feeds the planned v0.14 per-agent budgets and earned-autonomy specs, which are
> not written yet.
> Verified against: this repository at `main` (`8f5d86e`), kro v0.10.0-rc.0 (`54a203b`),
> `k8s.io/apiserver` and `k8s.io/client-go` v0.37.1 (the versions in `go.mod`; kro pins
> client-go v0.36.3, and the lines cited from it are the same in both). Source references are
> `path:line` in those trees. DORA definitions are from
> [dora.dev/guides/dora-metrics](https://dora.dev/guides/dora-metrics/), read 2026-10-15.

---

## 1. Problem and scope

kardinal computes one set of DORA figures per Pipeline (`Pipeline.status.deploymentMetrics`).
It cannot answer the question the agentic plan asks: **are the agents' changes as stable as the
people's?** DORA 2025 found that AI raises throughput and lowers stability (AGENTIC-PLAN §1), so
the split by actor is the measurement that tells a team whether an agent has earned more
autonomy, and what an agent costs in failures and rework.

Separately, every promotion Job and every read the controller makes for a Pipeline runs as a
shared identity: the controller's own ServiceAccount, the namespace's `default` ServiceAccount
for hooks, or `kardinal-render` for renders. Nothing in Kubernetes RBAC says "this Pipeline may
read that Deployment" or "this Pipeline's hook may call that cloud API".

| # | Piece | Sections |
|---|-------|----------|
| A | DORA by actor: deployment frequency, lead time, change fail rate, recovery time and rework rate, for humans as one group and for each agent | 4, 5 |
| B | Agent fleet view: which agents act in which Pipelines, with their budgets and failure rates | 6 |
| C | UI API endpoints and the two UI views, within the size budget | 7, 8 |
| D | Per-Pipeline promotion identity: Jobs, and the controller's reads for a Pipeline, run as a Pipeline's ServiceAccount | 9 |

**Non-goals.**

- Per-person metrics. Humans are one group (decision AA4). The principal stays on the Bundle and
  the AuditEvent, where audit needs it.
- History beyond the log's retention (100 deployments or 90 days per Pipeline by default).
  Long-term analytics read `kardinal audit export` (17-actors §7) into a warehouse.
- Prometheus series per agent. A label per agent and per Pipeline is unbounded cardinality;
  the API is the interface (section 13).
- New PolicyGate CEL variables. The earned-autonomy spec decides what gates read; section 10
  says what this spec gives it.
- Running the Graph (kro's applies) as the Pipeline's identity (decision AI1).

## 2. Terms

| Term | Meaning |
|------|---------|
| **Deployment** | A Bundle whose change reached a final environment (a DAG sink): its health check started with a change to apply. The definition of `computeStability` (`pkg/reconciler/pipeline/metrics.go:315-330`), unchanged. |
| **Actor bucket** | Where a deployment is counted: `human`, `bot`, `controller`, `unknown`, or `agent:<name>`. |
| **Author** | The Bundle's actor: its `spec.actor` stamp, or, without one, the kind derived from `kardinal.io/created-by` (5.2). |
| **Rework deployment** | A rollback Bundle, or a Bundle that declares the failed Bundle it fixes (`spec.provenance.fixOf`, 4.1). |
| **Promotion identity** | The ServiceAccount a Pipeline's Jobs run as, and as which the controller's reads for the Pipeline are authorized (section 9). |
| **Graph identity** | The ServiceAccount kro impersonates to apply a Bundle's Graph (`kardinal-graph`, per namespace; ledger G5). |

## 3. Graph-first

| Concern | Mechanism | AGENTS.md question |
|---------|-----------|--------------------|
| The per-deployment record | A `DeploymentLog` CR per Pipeline. Its reconciler reads the Pipeline's Bundles and PromotionSteps (status their own reconcilers wrote) and writes only `DeploymentLog.status` | 2: a reconciler that writes its own CRD status. Not a Graph node: a Graph is per Bundle and is retired or deleted with it (ledger G15), while the log outlives every Bundle |
| Aggregates over a time window | A pure function (`pkg/delivery.Summarize`) over the log's entries, called by the UI API, the CLI and the reconciler | Display, not a promotion decision: nothing gates on it in this spec |
| Promotion identity on Jobs | The translator copies the Pipeline's ServiceAccount into the HookRun and RenderRun templates (CRD fields the Graph carries); their reconcilers set the Pod's `serviceAccountName` | 2: the run CRs' reconcilers, as today |
| Naming a ServiceAccount | A ValidatingAdmissionPolicy with the `authorizer` library | VAP |
| The ServiceAccount's privileges | SubjectAccessReviews as the ServiceAccount, written to the Pipeline's and each run's own status | 2, the AgentRun pattern (19-verdicts §8.4) |

Two rows are added to the ledger's G8 table, marked proposed (section 16). No kro gap is
needed: section 9.8 says why the Graph identity stays per namespace, and what kro would do if
it did not.

## 4. What exists today (verified)

**Pipeline metrics.** The Pipeline reconciler lists the Pipeline's Bundles and PromotionSteps on
every reconcile (`pkg/reconciler/pipeline/reconciler.go:300-313`) and calls
`ComputeDeploymentMetrics` (`reconciler.go:334`). That function samples the last 30 deployments
(`metrics.go:44`), takes lead time from Bundle creation to Verified in the final environment
(`metrics.go:136-145`), the change failure rate and time to restore from `computeStability`
(`metrics.go:331-492`), and counts rollbacks from `spec.provenance.rollbackOf` (`metrics.go:151-157`).
It reads only objects that still exist.

**History that disappears.** The Bundle reconciler deletes the oldest terminal Bundles past
`spec.historyLimit`, default 50 (`pkg/reconciler/bundle/reconciler.go:621-623, 673-720`;
`api/v1alpha1/pipeline_types.go:66-72`), except the ones `heldHistory` keeps
(`bundle/reconciler.go:764`). A Bundle's PromotionSteps go with it, or with its retirement into
`status.retiredSteps` (#1492, `pkg/lifecycle/retired.go:162`). After 50 Bundles, the metrics of
the 51st are gone.

**AuditEvents.** They outlive Bundles, but retention deletes them after 90 days and past 1,000
per Pipeline (`pkg/reconciler/auditretention/retention.go:51-52`). The controller does not cache
them: nothing it runs reads them, and the CLI lists them directly (`cmd/kardinal/cmd/audit.go:86`).
The `actor` field arrives with #1857 (17-actors §7); stored v0.11 records have none.

**The actor.** 17-actors §4 stamps `spec.actor` on Bundles (kind, principal, agent, sponsor;
immutable). Agents must stamp, people and bots may omit it (17-actors §5.5), so an unstamped
Bundle created after v0.12 is never an agent's.

**The UI.** The Metrics tab is lazy (`web/src/App.tsx:64-67`): `ReleaseMetricsBar` shows the
Pipeline's `deploymentMetrics` plus figures it computes from the last 10 Bundles. The fleet board
(`web/src/components/FleetBoard.tsx:4-7`) draws each Pipeline as a line of stations from
`GET /api/v1/ui/pipelines`. `FleetHealthBar` buckets Pipelines client-side and its badges drive
the list filter (`FleetHealthBar.tsx:41-45`). `#/approvals` is a lazy page of its own
(`App.tsx:40`, `useUrlState.ts:44-45, 114`).

**The UI API and RBAC.** Every UI API read goes through `uiauth.AuthorizingClient`
(`pkg/uiauth/access.go:127-146`): an all-namespaces List returns only the items of namespaces
where the caller may list that kind (`access.go:207-226`). The OpenAPI document is generated from
the route table in `cmd/kardinal-controller/openapi_spec_test.go:75` and the Go types; tests fail
when a registered path is undocumented or a documented one fails (`openapi_spec_test.go:491-541,
563`).

**Who runs what.**

| Work | Runs as | Source |
|------|---------|--------|
| Graph applies (PromotionSteps, gate instances, HookRuns, RenderRuns) | kro, impersonating `kardinal-graph` in the Pipeline namespace | `pkg/graph/builder.go:77-80`; kro `pkg/controller/graph/impersonation.go:51-58` |
| Git and SCM steps (clone, edit, commit, push, PR) | the controller process, with the Pipeline's `spec.git.secretRef` Secret (same namespace only) | `api/v1alpha1/pipeline_types.go:126-149` |
| Health reads (Deployments, Argo CD, Flux), any namespace the Pipeline names | the controller's ServiceAccount (cluster-wide `get, list, watch`) | `pipeline_types.go:955-957`; `chart/kardinal-promoter/templates/_rbac.tpl:135-177` |
| Hook Job Pods | the hook's `serviceAccountName`, default `default`, which must be in `--hook-service-accounts`; never the Graph ServiceAccount | `pkg/reconciler/hookrun/reconciler.go:74-76, 641-660`; `cmd/kardinal-controller/main.go:464-467` |
| Render Job Pods | `kardinal-render`, created by the controller with no role and no token | `pkg/reconciler/renderrun/reconciler.go:56-61, 359-374, 454-455` |
| AgentRun Pods (planned, E8) | the agent's own ServiceAccount, proved read-only by SubjectAccessReviews | 19-verdicts §8.4 |

The controller creates every Job with its own identity (`_rbac.tpl:88-89`), holds `get` on every
Secret (`_rbac.tpl:34-38`) and may create ServiceAccounts and SubjectAccessReviews
(`_rbac.tpl:122-124, 274-276`). A Pipeline author who cannot read a Deployment in another
namespace can still point a health check at it and read its health from the Pipeline's status:
the controller is the confused deputy.

## 5. DORA by actor

### 5.1 The metrics

DORA's five metrics (dora.dev, 2026-10-15), and how each is computed per actor bucket over a
window (7, 30 or 90 days):

| DORA metric | DORA definition | kardinal, per bucket |
|-------------|-----------------|----------------------|
| Deployment frequency | "The number of deployments over a given period or the time between deployments." | Deployments by the bucket's authors, with `deployedAt` in the window. The UI shows per day or per week. |
| Change lead time | "...from committed to version control to deployed in production." | p50 and p90 of `verifiedAt - createdAt` (Bundle creation to Verified in every targeted final environment), as `deploymentMetrics` today. Bundle creation, not the commit: kardinal does not know when the commit was made unless `spec.provenance.timestamp` is set, and is not changed here. |
| Change fail rate | "The ratio of deployments that require immediate intervention following a deployment." | Failed deployments / deployments, failure as `computeStability` defines it (`metrics.go:318-322`). Integer thousandths. |
| Failed deployment recovery time | "The time it takes to recover from a deployment that fails and requires immediate intervention." | Mean minutes from a failed deployment's `deployedAt` to its restore (`metrics.go:324-330`), counted in the bucket of the **failed** deployment's author. Unrestored failures are counted apart. |
| Deployment rework rate | "The ratio of deployments that are unplanned but happen as a result of an incident in production." | Rework deployments by the bucket's authors / all deployments by them, rollbacks included in both. Plus `reworkCaused`: the rework deployments that fixed this bucket's failures (decision AA3). |

A bucket with fewer than 5 deployments in the window is marked `lowSample`; the UI shows its
figures muted and without a colour band.

### 5.2 Attribution and buckets

| Author | Bucket | Note |
|--------|--------|------|
| Stamp `kind: agent` | `agent:<actor.agent>`, with the stamp's sponsor | The sponsor at the time of the change (17-actors §4.1) |
| Stamp `kind: human` | `human` | |
| Stamp `kind: bot` | `bot` | The Bundle API's static token too |
| Stamp `kind: controller` | `controller` | Subscriptions and automatic rollbacks |
| No stamp, `kardinal.io/created-by` is a ServiceAccount (`system:serviceaccount:`) | `bot`, `verified: false` | |
| No stamp, any other `kardinal.io/created-by` (not `subscription:*`, not `kardinal-controller`) | `human`, `verified: false` | Agents must stamp (17-actors §5.5), so this is never an agent after v0.12 |
| No stamp, `subscription:*` or `kardinal-controller` | `controller`, `verified: false` | The v0.11 component names (17-actors §4.1) |
| Nothing | `unknown` | |

Each bucket reports `unverified`: how many of its deployments had no stamp. v0.11 Bundles are
attributed by their creator name as v0.11 recorded it; before v0.12 there were no registered
agents, so `human` is correct by definition (17-actors §4.2).

The log stores the kind, the agent and the sponsor, **not the human principal** (AA4).

### 5.3 Where the aggregates live: options

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `AgentIdentity.status.metrics`, per agent, written by the AgentIdentity reconciler | One place per agent; earned autonomy reads it next to `agent.*` | Humans have no object. The object is cluster-scoped and readable by every authenticated user (17-actors §3, `actors.directory.public`), so per-Pipeline detail would cross tenants. One object is written for every deployment of an agent in every namespace: a hot object at scale. It still needs a durable per-deployment source, because Bundles are deleted. |
| (b) | `Pipeline.status.deploymentMetrics.byActor`, computed by `ComputeDeploymentMetrics` from the same objects | No new kind; same function | Bounded by `historyLimit` (50) and the 30-deployment sample: a low-volume agent's history vanishes behind a busy one. The Pipeline status is the most often written object of the controller (#1719); every byte added is written on every step change. Windows by count, not time. |
| (c) | A `DeploymentLog` CR per Pipeline: a bounded list of per-deployment entries, merged from the live objects so entries survive their Bundles; aggregates computed from it | Survives `historyLimit`; time windows; tenant-local (namespaced, same RBAC as the Pipeline); written once per deployment transition, not per step change; the fleet view sums visible logs | One more CRD, about 25 KB per Pipeline in the controller's cache (section 13) |
| (d) | Computed in the UI API from AuditEvents on each request | No new state | Not cached, unbounded (90 days, up to 1,000 per Pipeline: 2 million objects at 2,000 Pipelines), so a page load would list them all. Writes can be dropped by the rate limit (17-actors §7). The stability rules would be re-implemented over events. No actor before v0.12. |
| (e) | An external time-series store (Prometheus, a warehouse) | Long history | Needs an install kardinal does not require; kubectl is no longer sufficient; agent × Pipeline cardinality |

**Decision AA1: (c).** It is the only option that keeps per-actor history past `historyLimit`
without unbounded objects, keeps tenant data in the tenant's namespace, and writes rarely. The
earned-autonomy spec can fold logs into `AgentIdentity.status` with a reconciler of its own,
which (a) needs anyway; this spec gives it the source.

### 5.4 The DeploymentLog CR

```yaml
apiVersion: kardinal.io/v1alpha1
kind: DeploymentLog                  # namespaced, short name deplog, category kardinal-all
metadata:
  name: checkout                     # the Pipeline's name
  namespace: payments
  ownerReferences: [{kind: Pipeline, name: checkout, uid: ..., controller: true}]
spec:
  pipeline: checkout                 # immutable
status:
  observedPipelineGeneration: 7
  finalEnvironments: [prod-eu, prod-us]   # as finalEnvironments() resolved them
  maxEntries: 100                    # --deployment-log-entries (10..500)
  maxAge: 2160h                      # --deployment-log-max-age (90 days)
  entries:                           # newest deployedAt first; one per Bundle UID
    - bundle: checkout-7f3c2a
      uid: 0f8e3c1a-6b1d-4a52-9f0e-2c7d9a1b3e44
      actor: {kind: agent, agent: fixer, sponsor: alice@example.com, verified: true}
      createdAt: "2026-10-15T10:00:00Z"
      deployedAt: "2026-10-15T10:41:12Z"   # first final-environment health check start
      verifiedAt: "2026-10-15T10:52:40Z"   # every targeted final environment Verified; unset until then
      failed: false
      failure: ""                    # StepFailed | AbortedByAlarm | RolledBack | Rejected
      rollbackOf: ""                 # set on rollback Bundles
      rollbackFrom: ""               # kardinal.io/rollback-from of a rollback Bundle
      fixOf: ""                      # spec.provenance.fixOf (4.1)
      targetEnvironment: ""          # a rollback of one environment
      final: true                    # the Bundle is terminal; the entry no longer changes from its own objects
      frozen: false                  # the Bundle is gone; kept as recorded
  actors:                            # over all entries (no window, no clock): for kubectl
    - key: agent:fixer
      deployments: 41
      failed: 3
      restored: 3
      rework: 2
      reworkCaused: 3
      unverified: 0
  conditions: [...]                  # Ready; Truncated (entries dropped by maxEntries); Reseeded
```

- **Bounded.** `entries` has `maxItems: 500`; names are capped as their source fields are. A
  typical entry is about 250 bytes of JSON, so the default log is about 25 KB and the largest
  (500 entries of 253-character names) under 400 KB, below etcd's 1.5 MiB.
- **One per Bundle, not per target.** A fleet environment with 200 targets is one entry, as in
  `computeStability`, which aggregates a Bundle's final-environment steps.
- **New field on Bundles.** `spec.provenance.fixOf` (optional, a Bundle name in the same
  Pipeline, at most 253 characters), immutable with the rest of `spec.provenance`
  (`api/v1alpha1/bundle_types.go:25`). Set by `kardinal create bundle --fix-of <bundle>`, the
  Bundle API's `fixOf` field and the MCP ship tool's `fixOf` argument. It is asserted by the
  author (AA3).

### 5.5 Derivation, merge and idempotency

`pkg/delivery` holds three pure functions; `computeStability` is refactored onto them, so the
existing `deploymentMetrics` keep their values (a property test compares the two on random
estates).

1. `Derive(pipeline, bundles, steps) []Entry`: one entry per live Bundle whose change reached a
   final environment, with the rules of `computeStability` (no-op promotions, supersession
   cancellations, `Failed`/`AbortedByAlarm`/`RollingBack`, rejection after deployment). Retired
   steps are included (`lifecycle.AddRetiredSteps`).
2. `Merge(log, derived, now) Log`: by UID, a derived entry replaces the stored one; a stored
   entry whose Bundle no longer exists is kept with `frozen: true`; entries past `maxAge` or
   beyond `maxEntries` are dropped, oldest `deployedAt` first. The result is sorted, so
   `Merge(Merge(l, d), d) == Merge(l, d)`: a re-run writes nothing.
3. `Summarize(entries, from, to) []ActorMetrics`: the metrics of 5.1 per bucket. Failures,
   restores and rework are decided over the entries, not the live objects: a rollback entry's
   `rollbackFrom` marks the named entry failed, and the restore of a failure is the first later
   entry Verified in every final environment it targets (the restore rule of
   `metrics.go:440-487`). So a failure whose Bundle was deleted is still restored by a later
   entry, and the rollback that restored it is still rework.

**Reconciler.** `pkg/reconciler/deploymentlog` creates the DeploymentLog of each Pipeline (owned
by it) and writes only its status. It is triggered by Bundle and PromotionStep events of the
Pipeline (the existing `kardinal.io/pipeline` label and `spec.pipelineName` index), with a
predicate that passes only changes of phase, state, `noChanges` and the health-check step's
`startedAt`, so a deployment is a handful of writes, not one per step update. It requeues at
the oldest entry's expiry, so pruning by age needs no other timer. It is idempotent by
construction (`Merge` is a fixed point) and sharded like the Pipeline reconciler
(`--namespace-shard`).

### 5.6 Keeping history: the `historyLimit` guard

A Bundle deleted by `historyLimit` before the log recorded its final state would lose its entry.
`enforceHistoryLimit` therefore keeps a terminal Bundle while its DeploymentLog entry is not
`final` (a read of the log through the cache, added to `heldHistory`), for at most one hour after
the Bundle became terminal. The hour bounds the guard if the log reconciler is down or the log
was deleted; past it the Bundle is deleted as today and the log is marked `Truncated`.

### 5.7 Windows at read time

The windows are computed when they are read, from the stored entries and the request's time,
not written to status: a status that changes only because time passes would be rewritten for
every Pipeline every hour. `status.actors` covers all entries and so needs no clock.

## 6. The agent fleet view

**What it answers.** Which agents act in which Pipelines, how often, how stable their changes
are there, and how close they are to their budgets.

**Data.**

| Column | Source |
|--------|--------|
| Agent, sponsor, team, autonomy, suspended, ready, scope | `AgentIdentity` spec and status (17-actors §3) |
| Budgets and their use | `AgentIdentity.spec.budgets` and `status.usage`, from the planned v0.14 budgets spec, passed through as name, limit, used, window. Omitted until that spec lands. |
| Per Pipeline: deployments, change fail rate, recovery, rework, last deployment | `Summarize` over each visible DeploymentLog's entries of that agent |
| In flight now | Bundles in phase `Promoting` whose stamp names the agent (the UI API already lists Bundles for the pipeline list) |
| Humans (comparison row) | `Summarize` over the visible logs' `human` entries |

**Tenancy.** Totals are sums over the logs of namespaces the caller may list (`AuthorizingClient`,
section 4). An agent active in namespaces A and B shows a caller who sees only A the figures of
A, and the response says `partial: true` when the caller cannot list every namespace. Budget use
comes from the AgentIdentity, which is cluster-wide and already readable by every authenticated
user by default (17-actors A10); with `actors.directory.public: false` and no grant, the row
shows the agent's name from the logs and `registered: unknown`.

**Reuse.** From `FleetHealthBar`: the summary-chips-as-filters pattern and the `filter` URL key.
From the design system: `StatusChip` (`web/src/ui/Chips.tsx`), `Tabs`, `Skeleton`,
`useRovingFocus`, `usePolling`. From `PipelineOpsTable`: its table stylesheet. The fleet board
itself is not changed: a station mark per agent was considered and rejected because
`FleetBoard` is in the first page's initial load (decision AA7).

## 7. UI API

Two read endpoints on the UI listener. Both are added to `apiRoutes`
(`cmd/kardinal-controller/openapi_spec_test.go:75`), so they are in the generated
`openapi.json` and `docs/reference/openapi.json`, to the endpoint table of
`docs/reference/rest-api.md`, and to the request fixtures of `TestOpenAPIRoutesSucceed`.

| Method | Path | operationId | Query |
|--------|------|-------------|-------|
| `GET` | `/api/v1/ui/pipelines/{pipeline}/actor-metrics` | `getPipelineActorMetrics` | `namespace`, `window` (`7d`, `30d`, `90d`; default `30d`) |
| `GET` | `/api/v1/ui/agents` | `listAgents` | `window`, `agent` (one agent's rows only) |

The first is a new case in `handlePipelinesSubpath` (`ui_api.go:880`), beside `/bundles`. The
second is a new `mux.HandleFunc` in `RegisterRoutes` (`ui_api.go:620-638`).

```go
// uiActorMetrics is one actor bucket's DORA figures over a window.
type uiActorMetrics struct {
    Key       string `json:"key"`               // human | bot | controller | unknown | agent:<name>
    Kind      string `json:"kind"`              // human | bot | controller | unknown | agent
    Agent     string `json:"agent,omitempty"`
    Sponsor   string `json:"sponsor,omitempty"` // the newest entry's sponsor
    LowSample bool   `json:"lowSample"`         // fewer than 5 deployments

    Deployments              int   `json:"deployments"`
    LeadTimeP50Minutes       int64 `json:"leadTimeP50Minutes"`
    LeadTimeP90Minutes       int64 `json:"leadTimeP90Minutes"`
    FailedDeployments        int   `json:"failedDeployments"`
    ChangeFailureRateMillis  int   `json:"changeFailureRateMillis"`
    RestoredFailures         int   `json:"restoredFailures"`
    UnrestoredFailures       int   `json:"unrestoredFailures"`
    MeanTimeToRestoreMinutes int64 `json:"meanTimeToRestoreMinutes"`
    ReworkDeployments        int   `json:"reworkDeployments"`
    ReworkRateMillis         int   `json:"reworkRateMillis"`
    ReworkCaused             int   `json:"reworkCaused"`
    Unverified               int   `json:"unverified"`  // deployments without an actor stamp
}

// uiActorMetricsResponse is GET /api/v1/ui/pipelines/{pipeline}/actor-metrics.
type uiActorMetricsResponse struct {
    Pipeline  string           `json:"pipeline"`
    Namespace string           `json:"namespace"`
    Window    string           `json:"window"`
    From      metav1.Time      `json:"from"`
    To        metav1.Time      `json:"to"`
    Truncated bool             `json:"truncated"` // the log does not reach back to From
    Oldest    *metav1.Time     `json:"oldest,omitempty"`
    Actors    []uiActorMetrics `json:"actors"`  // human first, then agents by deployments, then bot, controller, unknown
}

// uiAgentPipeline is one agent's activity in one Pipeline.
type uiAgentPipeline struct {
    Namespace        string         `json:"namespace"`
    Pipeline         string         `json:"pipeline"`
    Metrics          uiActorMetrics `json:"metrics"`
    LastDeployedAt   *metav1.Time   `json:"lastDeployedAt,omitempty"`
    InFlight         []string       `json:"inFlight,omitempty"` // Bundle versions Promoting now, at most 5
}

// uiAgentBudget is one budget of the planned budgets spec, passed through.
type uiAgentBudget struct {
    Name   string `json:"name"`
    Limit  int    `json:"limit"`
    Used   int    `json:"used"`
    Window string `json:"window"`
}

// uiAgentRow is one agent.
type uiAgentRow struct {
    Name       string            `json:"name"`
    Registered string            `json:"registered"` // yes | deleted | unknown (directory not readable)
    Sponsor    string            `json:"sponsor,omitempty"`
    Team       string            `json:"team,omitempty"`
    Autonomy   string            `json:"autonomy,omitempty"`
    Suspended  bool              `json:"suspended"`
    Ready      bool              `json:"ready"`
    Totals     uiActorMetrics    `json:"totals"`              // over the visible Pipelines
    Pipelines  []uiAgentPipeline `json:"pipelines"`           // by deployments, at most 200
    Budgets    []uiAgentBudget   `json:"budgets,omitempty"`
}

// uiAgentsResponse is GET /api/v1/ui/agents.
type uiAgentsResponse struct {
    Window     string         `json:"window"`
    Partial    bool           `json:"partial"`    // the caller cannot list every namespace
    Pipelines  int            `json:"pipelines"`  // visible Pipelines with a DeploymentLog
    Humans     uiActorMetrics `json:"humans"`
    Agents     []uiAgentRow   `json:"agents"`     // registered agents first, then deleted ones seen in logs
}
```

**Behaviour.** Both read through the cached, authorizing client: DeploymentLogs, AgentIdentities
and (for in-flight) Bundles. `window` outside the three values is a 400. A Pipeline without a log
yet answers 200 with no actors. The fleet response is computed from cached objects with no
API calls: at 2,000 Pipelines × 100 entries that is 200,000 entries, a single pass
(`BenchmarkListAgents`, section 17). The Agents page polls every 30 s, not every 5 s.

**RBAC.** The chart's `viewer` role gains `get, list, watch` on `deploymentlogs`. Nobody but the
controller writes them (section 11).

## 8. UI views and the size budget

### 8.1 The Metrics tab gains "By actor"

Under the existing release-metrics bar, a table titled "By actor", with a 7 / 30 / 90 days
`Tabs` selector (the choice is kept in the URL as `?tab=metrics&w=30d`). One row per bucket:
Humans, then each agent ("agent fixer, for alice@example.com"), then Bots, kardinal (automation)
and Unknown when they have deployments. Columns: Deploys (per week), Lead time (p50 / p90),
Change fail rate, Recovery (mean, with "2 not restored" when some are open), Rework rate, and
"Caused rework" (how many rollbacks and fixes this actor's failures needed). Rates use the
`cfrColor` bands `ReleaseMetricsBar` already has (`ReleaseMetricsBar.tsx:121-125`) unless the
row is `lowSample`. A footnote says how many versions had no actor stamp and were counted by
their creator, and, when `truncated`, "covers the last 100 deployments, since 3 Sep". Agent names
link to the Agents page filtered on that agent.

### 8.2 The Agents page (`#/agents`)

A topbar link "Agents" next to "Approvals", shown when the directory is readable and at least
one AgentIdentity exists or a visible log has an agent entry.

- **Summary chips**, the `FleetHealthBar` pattern, each a filter: "6 agents", "4 active in 30
  days", "1 suspended", "1 over a budget", "2 above 15% change fail rate".
- **A Humans row**, pinned first, for comparison.
- **One row per agent**: name and sponsor; autonomy chip; status chip (ready, suspended, not
  ready, deleted); "acts in 12 pipelines"; deploys per week; change fail rate; recovery; rework
  rate; each budget as "used / limit" with a meter; "in flight: 2".
- **Expanding a row** lists its Pipelines (namespace / name, linking to `#/p/<ns>/<name>`), each
  with deploys, change fail rate, last deployment and the versions in flight.
- **Empty state**: "No agents are registered", with a link to the actors guide.
- With `partial: true`: "Showing the 14 namespaces you can see."

### 8.3 Size budget

Measured on `main` with `node web/scripts/check-size.mjs` (gzip): initial JS 128.9 KB of 152,
total JS 159.8 of 200, initial CSS 16.7 of 18, **total CSS 24.1 of 25**, fonts 156.9 of 180.
For scale: the Approvals page chunk is 2.1 KB of JS and 0.7 KB of CSS, the operations table
2.0 KB and 0.7 KB.

| Part | Loaded | Estimate (gzip) |
|------|--------|-----------------|
| `AgentsPage` chunk | lazily, on `#/agents` (`React.lazy`, `Skeleton` sized as the table) | JS ≤ 3.5 KB, CSS ≤ 0.4 KB |
| `ActorMetricsTable` chunk | lazily, when the Metrics tab opens (beside `ReleaseMetricsBar`) | JS ≤ 1.5 KB, CSS ≤ 0.1 KB |
| Route parsing, the topbar link, the two lazy stubs | entry | JS ≤ 0.3 KB |

- **Initial JS**: +0.3 KB on every route. The `#/agents` route loads the entry plus `AgentsPage`,
  smaller than the `/env/` link that sets today's initial figure. `ROUTE_VIEWS` in
  `web/scripts/check-size.mjs:57` gains `'an /agents link': ['AgentsPage']` so it is measured.
- **Total JS**: about 165 KB of 200.
- **Total CSS is the binding limit**: 0.9 KB left. The two views reuse the operations table's
  stylesheet and the design-system classes and add at most 0.5 KB. If the implementation does
  not fit, it removes CSS elsewhere in the same PR: the budgets move only at a release
  re-baseline (CONTRIBUTING.md, "size" section).
- No chart library and no new dependency. The meters are a styled `<meter>`.

## 9. Per-Pipeline promotion identity

### 9.1 What changes for whom

With `spec.identity` set, the Pipeline's ServiceAccount is:

1. **The Pod identity of its Jobs**: hook Jobs, render Jobs and, when the StepTemplate spec
   lands, its custom steps. AgentRuns keep the agent's own ServiceAccount (19-verdicts §8.4): an
   agent's verifier is the agent, not the Pipeline.
2. **The subject the controller authorizes its reads for the Pipeline against**: the git
   Secret, the kubeconfig Secret of a remote health check, and the health targets (Deployments,
   Argo CD Applications, Flux Kustomizations) in whatever namespace the Pipeline names.

It is not the Graph identity (9.8), and not the identity of SCM calls (those use the git Secret
or the SCM provider, as today).

### 9.2 Options for the scope

| | Scope | For | Against |
|-|-------|-----|---------|
| (a) | The Graph runs as the Pipeline's ServiceAccount (`Graph.spec.serviceAccountName`) | kro enforces the identity natively on every apply | The Graph's content is generated by the translator, not the author, so the applies gain little. The Pipeline SA would need the applier role (create PromotionSteps, gate instances), and a Pod running as it would hold that role: a hook could forge PromotionSteps. The G16 policies tell kro from others by the absence of Pod-token extras, which breaks when the same SA is also a Pod identity. One RoleBinding per Pipeline from `IdentityProvisioner`. |
| (b) | Only the Jobs' Pods | Small; Kubernetes runs the Pod as the SA | The controller's reads for the Pipeline stay a confused deputy |
| (c) | The Jobs' Pods, and the controller's reads for the Pipeline authorized as the SA | Closes the confused deputy; one identity for everything the Pipeline touches in the cluster; the Graph keeps a separate, non-Pod identity | SubjectAccessReviews in reconcilers (9.7) |

**Decision AI1: (c).**

### 9.3 API

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
spec:
  identity:                          # optional; unset = the shared identities of today
    serviceAccountName: checkout-promoter   # an existing ServiceAccount in this namespace; or
    create: true                     # the controller creates kardinal-pipeline-<pipeline>
status:
  identity:
    serviceAccount: checkout-promoter
    ready: true
    reason: ""                       # ServiceAccountMissing | GraphIdentity | Privileged: <what> | NotOwned
    checkedAt: "..."
  conditions: [{type: IdentityReady, ...}]
```

CRD rules: exactly one of `serviceAccountName` and `create`; the name is a DNS-1123 subdomain of
at most 253 characters. The chart value `promotionIdentity.requireOnCreate` (default `false`)
adds a VAP rule that refuses a new Pipeline without `spec.identity`.

### 9.4 Who creates the ServiceAccount and its RBAC

| | Who | For | Against |
|-|-----|-----|---------|
| (a) | The controller creates the SA and binds roles from a template ClusterRole | Zero setup | The controller would need `bind` on arbitrary roles or `escalate`; it could grant what the Pipeline author cannot (ledger G5 explains why kro refuses the same) |
| (b) | The namespace admin creates the SA and its RBAC; the Pipeline names it | Least power for kardinal; works with existing workload-identity setups (cloud annotations on the SA) | Setup per Pipeline |
| (c) | The controller creates an empty SA (`create: true`); the namespace admin binds roles to it | One field; nothing granted by kardinal | The name is fixed (`kardinal-pipeline-<pipeline>`, truncated with a hash past 253 characters) |

**Decision AI2: (b) and (c), never (a).** The controller already creates ServiceAccounts
(`_rbac.tpl:122-124`) and needs nothing new for (c). It creates the SA with the labels
`app.kubernetes.io/managed-by: kardinal-promoter` and `kardinal.io/pipeline: <name>`, an
ownerReference to the Pipeline (deleted with it), and `automountServiceAccountToken: false`. It
**refuses to adopt** an existing ServiceAccount of that name that it did not create (no
ownerReference to this Pipeline's UID): `IdentityReady=False reason=NotOwned`. Otherwise a
Pipeline named `x` could claim a privileged ServiceAccount someone had named
`kardinal-pipeline-x`.

### 9.5 Admission: naming a ServiceAccount

The risk: the controller creates the Jobs, so a Pipeline author who may not create Pods could
otherwise run a hook as any ServiceAccount of the namespace by naming it. Kubernetes and kro
treat "may create Pods (or Graphs) in a namespace" as "may act as any ServiceAccount there"
(kro `website/docs/docs/advanced/01-access-control.md:110-124`). kardinal's user roles grant no
Pod creation, so naming must be a permission of its own.

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | None: the namespace is the boundary, as kro does | Simple | A `promoter` (no Pod rights) gains "run code as SA X" |
| (b) | Controller flag allowlist of SA names (as `--hook-service-accounts`) | Exists for hooks | Cluster-wide names, not per namespace or per person; the admin of each namespace cannot delegate |
| (c) | A VAP: the requester must be allowed the verb `use` on `serviceaccounts.kardinal.io/<name>` in the namespace | Per namespace and per name, delegable with a Role and `resourceNames`; checked by the API server's authorizer at write time | A new virtual permission to grant to GitOps appliers |
| (d) | The requester must hold `impersonate` on the ServiceAccount | Precise semantics | `impersonate` also lets them use `kubectl --as` against the API server: far more than naming it for kardinal |

**Decision AI3: (c).** The policy `<release>-pipeline-identity` matches CREATE and UPDATE of
`pipelines`:

```yaml
variables:
  - name: controller
    expression: request.userInfo.username in [...]          # this release's controller
  - name: sa
    expression: >-
      has(object.spec.identity) && has(object.spec.identity.serviceAccountName)
        ? object.spec.identity.serviceAccountName : ''
  - name: oldSa
    expression: >-
      oldObject != null && has(oldObject.spec.identity) && has(oldObject.spec.identity.serviceAccountName)
        ? oldObject.spec.identity.serviceAccountName : ''
validations:
  - expression: variables.sa == '' || !(variables.sa in ['{{ graph.serviceAccountName }}', 'default'])
    message: a Pipeline cannot run as the Graph ServiceAccount or the namespace's default ServiceAccount
  - expression: >-
      variables.sa == '' || variables.sa == variables.oldSa || variables.controller ||
      authorizer.group('kardinal.io').resource('serviceaccounts')
        .namespace(object.metadata.namespace).name(variables.sa).check('use').allowed()
    messageExpression: >-
      'you may not use ServiceAccount ' + variables.sa + ' for kardinal: ask the namespace admin for ' +
      '"use" on serviceaccounts.kardinal.io/' + variables.sa
```

- `authorizer` is a VAP variable (`k8s.io/apiserver/pkg/admission/plugin/cel/compile.go:38`),
  bound to the requester (`activation.go:81`), and the Authz library is in every Kubernetes
  version from 1.27 (`pkg/cel/environment/base.go:94-96`), so the 1.35 floor has it. RBAC
  verbs and resources are free strings: `serviceaccounts` in group `kardinal.io` is a virtual
  resource, as `policygates/override` already is (`chart/kardinal-promoter/templates/user-roles.yaml:27-28`).
- The check runs only when the name changes, so unrelated edits (pause, holds, a GitOps
  re-apply of the same spec) are unaffected.
- `create: true` needs no check: its name is fixed and adoption is refused (9.4).
- The chart adds the ClusterRole `<release>-identity-user` (`use` on `serviceaccounts` in
  `kardinal.io`), aggregated to `admin` (`rbac.authorization.k8s.io/aggregate-to-admin: "true"`),
  so a namespace admin may use, and delegate per name, the ServiceAccounts of their namespace.
  The `promoter` role does not get it. GitOps appliers that write Pipelines need it (documented).
- The check is at write time, like a Deployment's: revoking the grant later does not change a
  Pipeline that already names the SA. Removing the SA's own RoleBindings does.

### 9.6 Privilege checks on the ServiceAccount

A namespace admin may grant the SA anything. Some grants would let a hook bypass the governance
the Pipeline exists for: approve its own promotion, override a gate, ship a version, or act as
someone else. Before any Job runs as it, the SA is checked with SubjectAccessReviews, as
AgentRun checks an agent (19-verdicts §8.4). It must **not** be allowed, in the Pipeline
namespace unless noted:

| Verb | Resource | Why |
|------|----------|-----|
| `create` | `approvals` | approve its own promotion |
| `update` | `policygates/override`, `policygates` | override or rewrite a gate |
| `create` | `bundles`, `verdicts` | ship a version, or vote as a verifier |
| `update`, `patch` | `pipelines`, `pipelines/pause`, `pipelines/hold` | change its own policy |
| `create`, `update`, `patch` | `graphs` (`kro.run`), `promotionsteps` | forge Graph state |
| `impersonate` | `serviceaccounts`, `users`, `groups` (cluster scope) | act as others |
| `bind`, `escalate` | `roles`, `clusterroles` (cluster scope) | grant itself more |
| `create` | `serviceaccounts/token` | mint tokens for other SAs |

The list is in code (`pkg/identity`), with a test per row. A wide grant fails closed:
`IdentityReady=False reason="Privileged: checkout-promoter may create approvals in payments"`.

**Where it runs.** The Pipeline reconciler checks on every spec change and every 30 minutes,
and writes the result to `status.identity` and the `IdentityReady` condition, so the problem is
visible before any Bundle. Each Job-creating reconciler (HookRun, RenderRun, later StepTemplate
runs) checks again once per run start, through the same cached reviewer (`uiauth`'s
`NewCachedAccessReviewer`, 60 s TTL), and fails the run with the reason in its own status, as
AgentRun does with `Bounded=False`. Decision AI4.

### 9.7 The controller's reads for the Pipeline

With `spec.identity` set, before the controller reads, for a Pipeline, a Secret (`spec.git.secretRef`,
`health.kubeconfigSecretRef`) or a health target, it asks a SubjectAccessReview as the
Pipeline's SA (`get` on that object, its namespace and name), then reads with its own cached
client as today. This is `uiauth.AuthorizingClient` with the SA as the user (`access.go:127-146`),
the same wrapper the UI API uses for people. A denial fails the step with the reason
(`IdentityForbidden: checkout-promoter may not get deployments/checkout in payments-prod`). The
existing rules stay: same-namespace git Secrets (`pipeline_types.go:144-148`) and the
`kardinal.io/referenceable` label.

These reviews go to the API server, not to an external system, and are cached for 60 s: at
most one review per Pipeline, verb and object per minute. They are made in the PromotionStep
reconciler, which already makes uncached Secret GETs (`_rbac.tpl:34-36`).

### 9.8 kro: why the Graph identity stays per namespace

kro applies a Graph as `system:serviceaccount:<graph-namespace>:<spec.serviceAccountName>`,
confined to the Graph's namespace (`pkg/controller/graph/impersonation.go:51-58`), and refuses
its own controller ServiceAccount (`controller.go:745-749`). A per-Pipeline Graph identity is
possible in kro and was examined:

- kro caches one impersonated executor per username in an LRU of 1,024
  (`impersonation.go:66, 118-147`). Past 1,024 identities with live Graphs, entries are evicted
  and rebuilt. A rebuild is a client construction sharing the manager's REST mapper
  (`cmd/controller/graphengine.go:78-101`); the HTTP transport is shared, because
  impersonation is a round-tripper wrapper (`k8s.io/client-go/transport/round_trippers.go:64-68`)
  and not part of the transport cache key (`transport/cache.go:56`). So it costs CPU, not
  connections. This is not a gap for kardinal and gets no ledger entry.
- The real objections are kardinal's (9.2 a): the Pod identity and the Graph identity must
  differ, and the G16 policies rely on the Graph identity never being a Pod's.

Running Jobs as kro Job nodes under the Pipeline's identity would let kro enforce Pod creation
natively, but kro's delete and prune orphan a Job's Pods and re-run a deleted Job (ledger G12),
which is why HookRun and RenderRun exist. G12 is unchanged by this spec.

## 10. CEL context additions

None. Section 5 produces data a later spec can expose. The constraint it must respect: a
DeploymentLog is per Pipeline and per tenant, so `agent.changeFailureRate` across Pipelines
needs a reconciler that folds the logs into a status the PolicyGate reconciler reads (for
example `AgentIdentity.status.trackRecord`), never a computation inside the gate.

## 11. RBAC and admission summary

| Object | Writers | Readers |
|--------|---------|---------|
| `DeploymentLog` (spec and status) | the controller only: a rule in `<release>-graph-objects`'s style admits CREATE and UPDATE only from this release's controller (`chart/kardinal-promoter/templates/identity-admission.yaml:370`) | `viewer` and up, in the namespace; the UI API through `AuthorizingClient` |
| `Pipeline.spec.identity` | anyone who may update the Pipeline, plus `use` on `serviceaccounts.kardinal.io/<name>` (9.5) | as the Pipeline |
| The `kardinal-pipeline-<name>` SA | the controller creates it (existing `create serviceaccounts`) | |
| Its RoleBindings | the namespace admin only | |
| Controller | no new rules: `create subjectaccessreviews` and `create serviceaccounts` exist | |

## 12. Failure modes

| Failure | Effect | Handling |
|---------|--------|----------|
| Controller down while Bundles finish | Log not updated | Catches up from the live objects at start; `historyLimit` cannot delete in the meantime (the controller does that) |
| Log reconciler lags behind `historyLimit` | Entry would be lost | The guard (5.6) keeps the Bundle up to 1 h; past it `Truncated` |
| An admin deletes a DeploymentLog | History lost | Recreated and seeded from the live objects, condition `Reseeded`; the UI says "since <date>" |
| A Bundle name reused after deletion | Two entries | Keyed by UID |
| A deleted AgentIdentity | Its entries remain | Row `registered: deleted`; figures still shown |
| An agent omits `fixOf` on a fix | Rework rate understated | `reworkCaused` and the change fail rate come from verified data; the docs say rework is partly asserted |
| The Pipeline's SA is deleted | Jobs cannot start | `IdentityReady=False reason=ServiceAccountMissing` before a run; the run fails with it |
| The SA gains a forbidden grant | Governance bypass | Next check (spec change, 30 min, or run start) fails closed |
| The API server's authorizer is slow or errors | Reads and runs wait | Review errors are not cached and fail the step or run with a retryable reason |

## 13. Scale

Targets from the scale suite: 2,000 Pipelines, 2,000 AgentIdentities, 200 targets per fleet.

- **Cache**: about 25 KB per DeploymentLog at the default 100 entries, so about 50 MB at 2,000
  Pipelines. The scale test asserts the controller's memory grows by at most 60 MB.
- **Writes**: a few per deployment (deployed, verified or failed, frozen), against one write per
  step update today for the Pipeline status. A 200-target fleet wave is still one entry.
- **Reads**: `GET /api/v1/ui/agents` is one pass over the visible entries, no API calls;
  target p95 under 200 ms at 2,000 × 100 (benchmark and scale test).
- **Reviews**: privilege checks are about 15 reviews per Pipeline per 30 minutes: 17 a second at
  2,000 Pipelines with identities, plus one per run start (cached 60 s). Authorization reviews
  are answered by the API server's in-memory authorizers and touch no etcd.
- **Prometheus**: no per-agent series (non-goals). `kardinal_deployment_log_entries` (gauge, no
  labels) and `kardinal_deployment_log_truncations_total` are added.

## 14. Upgrade path

**From v0.13 (and v0.11 data).**

1. The `deploymentlogs` CRD and the changed `bundles` (`provenance.fixOf`) and `pipelines`
   (`spec.identity`, `status.identity`) CRDs are applied before the chart, as every upgrade
   requires.
2. The controller creates one DeploymentLog per Pipeline and seeds it from the retained
   Bundles and steps: history starts with what `historyLimit` kept. Unstamped Bundles are
   attributed as 5.2 says.
3. `Pipeline.status.deploymentMetrics` is unchanged (same values, same fields). Computing it
   from the log, so that it too survives `historyLimit`, is a follow-up issue that changes its
   documented sample.

**From the shared identities to per-Pipeline.** Nothing changes until a Pipeline sets
`spec.identity`:

1. The namespace admin creates the SA (or sets `create: true`) and binds what the hooks and
   health reads need. `kardinal doctor --pipeline <p>` (the flag exists; this check is new)
   lists what the Pipeline's current hooks, Secrets and health targets will need (the reviews
   of 9.7, as a dry run).
2. The Pipeline sets `spec.identity`. Bundles already in flight keep the identity their Graph
   was built with (the translator copies the SA into the run templates at translation time);
   new Bundles use the new one.
3. Hooks that set no `serviceAccountName` now run as the Pipeline's SA instead of `default`.
   A hook that names another SA still needs `--hook-service-accounts`. Render Jobs run as the
   Pipeline's SA instead of `kardinal-render`, still with no API token.
4. A cluster can require identities for new Pipelines with `promotionIdentity.requireOnCreate`.
   Existing Pipelines without one get the warning condition `IdentityShared`. A later release
   may make identities the default; that is not decided here.

Downgrading to v0.13 leaves DeploymentLogs inert (owned by their Pipelines) and ignores
`spec.identity` (the old CRD prunes the field on the next write).

## 15. What this does not protect against

- **Asserted rework.** `fixOf` is the author's statement. An agent that never declares fixes
  shows a lower rework rate; its failures still show in the change fail rate and in the
  `reworkCaused` of its row.
- **Unregistered agents** are counted as humans or bots (17-actors §11).
- **Small samples.** A rate over 3 deployments means little; the UI marks it, but a reader can
  still over-read it.
- **A namespace admin** can grant the Pipeline's SA anything outside the 9.6 list, for example
  write access to production Deployments. That is their decision; kardinal makes it visible
  (`kardinal doctor`) but does not judge it.
- **Write-time naming check.** Revoking `use` later does not unbind an existing Pipeline from
  its SA.
- **The controller's own identity.** Reads for a Pipeline are authorized as its SA, but made
  with the controller's credentials; a bug in the wrapper would read with the controller's
  rights. The deliberately-broken assertion tests of section 18 exist for that.

## 16. Ledger

No new G entry (9.8). This PR adds two rows to the
[G8 table](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph), marked proposed:
like every new G8 row, they need the owner's approval.

| Logic | Where | Why not in the Graph | Possible kro primitive |
|-------|-------|----------------------|------------------------|
| Per-actor deployment log (spec 28) | `pkg/reconciler/deploymentlog` writes `DeploymentLog.status` from the Pipeline's Bundles and steps | A Graph is per Bundle and is retired or deleted with it (G15); the log must outlive every Bundle. Display data: no promotion decision reads it | None needed |
| Promotion identity checks (spec 28) | `pkg/identity`: SubjectAccessReviews as the Pipeline's SA in the Pipeline, PromotionStep, HookRun and RenderRun reconcilers, results in their own status | kro authorizes only its own applies; the reads and Jobs are the reconcilers' | None needed while Jobs cannot be kro nodes (G12) |

## 17. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| AA1 | Where per-actor aggregates live | AgentIdentity status; Pipeline status; DeploymentLog CR; AuditEvents in the UI API; external store | **DeploymentLog** (5.3): survives `historyLimit`, tenant-local, rare writes, bounded |
| AA2 | Whose is a deployment's failure and recovery | the deployment's author; the approver; whoever restored it | **The author**, with recovery time in the failed deployment's bucket: the actor whose change failed owns the failure |
| AA3 | What counts as rework | rollbacks only; rollbacks and any Bundle that restores a failure; rollbacks and declared fixes | **Rollbacks and declared fixes (`fixOf`)**, plus `reworkCaused`: the restore heuristic would mark planned work that happened to come next as rework |
| AA4 | Humans | one group; per person; not shown | **One group**, no human principals in the log: the governance question is agents against people, and per-person delivery metrics invite misuse |
| AA5 | Unstamped records | `unknown`; derive from `created-by`; leave out | **Derive, with an `unverified` count**: agents must stamp, so the derivation cannot hide an agent after v0.12 |
| AA6 | Time windows | written to status; computed at read time; count-based | **Read time** (5.7): no status rewrites as time passes |
| AA7 | Where the fleet view goes | marks on the fleet board; a new lazy `#/agents` page; a column in the operations table | **New lazy page**: the fleet board is in the first page's initial JS, and the operations table is per Pipeline |
| AI1 | What runs as the Pipeline's identity | the Graph; Jobs only; Jobs and the controller's reads | **Jobs and the reads** (9.2): closes the confused deputy; keeps the Graph identity a non-Pod identity (G16) |
| AI2 | Who creates the SA and RBAC | controller with templated RBAC; the admin; the controller creates an empty SA | **The admin, or an empty controller-created SA**; kardinal never grants RBAC (9.4) |
| AI3 | Admission for naming an SA | none; flag allowlist; VAP `use` on `serviceaccounts.kardinal.io`; `impersonate` | **VAP with `use`** (9.5): per namespace and name, delegable, without `kubectl --as` |
| AI4 | A privileged SA | allow; warn; refuse a fixed list | **Refuse the 9.6 list**, fail closed, as AgentRun |
| AI5 | Rollout | default on; opt-in; required | **Opt-in**, with `requireOnCreate` for clusters that want it (14) |

## 18. Test plan

**Unit** (table-driven, `-race`):

- `pkg/delivery`: `Derive` for every `computeStability` case (no-op, supersession cancellation,
  `RollingBack`, `AbortedByAlarm`, rejected after deployment, fan-out final environments, a
  single-environment rollback, retired steps); `Merge` is a fixed point; frozen entries survive
  deletion; a restore and a rollback found across frozen entries; pruning by age and count;
  `Summarize` per bucket and window; `lowSample`; buckets for every row of 5.2. A property test:
  `Summarize` over a log seeded from live objects equals `computeStability` on them.
- The DeploymentLog reconciler: creation and ownership, the event predicate (a step-message
  update writes nothing), the requeue at expiry, idempotency (a second reconcile writes
  nothing), size at 500 entries of 253-character names.
- `enforceHistoryLimit` keeps a Bundle whose entry is not final, and deletes it after the hour.
- `pkg/identity`: each 9.6 row refused; the cache; review errors not cached.
- The HookRun and RenderRun Pod built with the Pipeline's SA; RenderRun still without a token.
- The UI API handlers: both endpoints, `window` validation, a caller who sees one of two
  namespaces gets that namespace's totals and `partial: true`; the OpenAPI tests.
- Web (vitest): the by-actor table and the Agents page models, the empty and partial states,
  keyboard navigation; `npm run size` with the new route.

**Admission** (the chart's policy on a real API server, as 17-actors §13):

| # | Requester | Change | Expect |
|---|-----------|--------|--------|
| 1 | promoter without `use` | sets `serviceAccountName: x` | denied, the message names the grant |
| 2 | namespace admin | sets `x` | admitted (aggregated role) |
| 3 | promoter with `use` on `x` only | sets `y` | denied |
| 4 | promoter without `use` | pauses a Pipeline already naming `x` | admitted |
| 5 | anyone | `kardinal-graph` or `default` | denied |
| 6 | anyone | `create: true` | admitted |
| 7 | a user | creates or updates a DeploymentLog | denied |
| 8 | `requireOnCreate: true` | a new Pipeline without `identity` | denied |

**Fake-client journeys:**

| # | Case | Expect |
|---|------|--------|
| J1 | 10 human and 10 agent Bundles through test, uat, prod; 2 agent Bundles fail in prod, one rolled back by a person, one fixed by a human `fixOf` Bundle | agent CFR 200‰, recovery from each failure to its restore; human rework 2 of 12; agent `reworkCaused` 2 |
| J2 | J1, then 60 more Bundles with `historyLimit: 20` | the per-actor figures over the first 20 are unchanged; `deploymentMetrics` as today |
| J3 | A v0.11-style unstamped Bundle by `alice` and one by `system:serviceaccount:ci:builder` | `human` and `bot`, both `unverified` |
| J4 | Pipeline with `create: true`; a hook without `serviceAccountName` | the hook Pod runs as `kardinal-pipeline-<p>` |
| J5 | The SA may `create approvals` | `IdentityReady=False`; the hook run fails with the reason |
| J6 | A health target in a namespace the SA may not read | the step fails with `IdentityForbidden` |

**Live e2e** (kind, COUNT=2, rows for `test/e2e/coverage.tsv` once implemented; suite `core`
unless noted):

| Proposed id | Feature |
|-------------|---------|
| `ANALYTICS-ACTOR-01` | Agent and human Bundles promote; `GET .../actor-metrics` shows both buckets with the expected counts |
| `ANALYTICS-ACTOR-02` | A rollback of an agent's Bundle: agent change fail rate and `reworkCaused`, human rework |
| `ANALYTICS-ACTOR-03` | Figures survive `historyLimit` deletion of the Bundles they came from |
| `ANALYTICS-FLEET-01` | `GET /api/v1/ui/agents` across two tenant namespaces: a tenant sees only its own, `partial: true` |
| `ANALYTICS-UI-01` | The Agents page and the by-actor table render (Playwright, `ui` suite) |
| `IDENTITY-PIPE-01` | The admission cases 1 to 6 |
| `IDENTITY-PIPE-02` | A hook and a render run as the Pipeline's SA (Pod `spec.serviceAccountName`, no token in the render Pod) |
| `IDENTITY-PIPE-03` | A privileged SA is refused before any Job is created |
| `IDENTITY-PIPE-04` | A health target the SA may not read fails the step; granting it lets a retry pass |
| `UPGRADE-0130-ANALYTICS` | Upgrade from v0.13.0: logs seeded, `deploymentMetrics` unchanged, shared identities unchanged (`upgrade` suite) |

Each safety assertion is shown to fail with the property broken on purpose (the namespace filter
removed, the 9.6 check skipped, the VAP rule dropped) and to pass once reverted.

**Scale** (`TestScale_*`, under the heavy-load lock): 2,000 Pipelines × 100 entries, 50 agents:
controller memory delta at most 60 MB; `listAgents` p95 under 200 ms; DeploymentLog writes per
deployment at most 4. The enterprise multi-tenant scenario (#1860) gains the tenant-isolation
check of `ANALYTICS-FLEET-01` across its 20 teams.

## 19. Work breakdown (issues, milestone v0.14.0)

1. `feat(api)`: the `DeploymentLog` CRD, `BundleProvenance.fixOf` and `--fix-of` on
   `kardinal create bundle`, the Bundle API and the MCP ship tool. (size m)
2. `refactor(pipeline)`: `pkg/delivery` (`Derive`, `Merge`, `Summarize`) with `computeStability`
   on it, values unchanged, property test. (m)
3. `feat(controller)`: the DeploymentLog reconciler, the `historyLimit` guard, the controller-only
   admission rule, viewer RBAC, the two gauges. (m)
4. `feat(api)`: `getPipelineActorMetrics` and `listAgents`, OpenAPI, `docs/reference/rest-api.md`. (m)
5. `feat(ui)`: the by-actor table in the Metrics tab and the `#/agents` page, lazy, with the
   `check-size.mjs` route and the size delta in the PR. (m)
6. `feat(cli)`: `kardinal metrics <pipeline> --by-actor` and `kardinal agents`; the MCP read tool
   `agents`. (s)
7. `feat(api)`: `Pipeline.spec.identity` and `status.identity`; the translator copies the SA into
   HookRun and RenderRun templates; Pods run as it. (m)
8. `feat(chart)`: the `<release>-pipeline-identity` policy, the `identity-user` ClusterRole
   aggregated to `admin`, `promotionIdentity.requireOnCreate`. (s)
9. `feat(controller)`: `pkg/identity` privilege checks in the Pipeline, HookRun and RenderRun
   reconcilers; the controller's reads authorized as the SA; `kardinal doctor --pipeline`. (l)
10. `docs`: "Analytics by actor" and "Promotion identity" user pages, the security guide, the
    upgrade notes, the changelog. (s)
11. `test`: the live e2e rows above, the scale test, the v0.13.0 upgrade test. (m)
12. Follow-up, not v0.14: `deploymentMetrics` computed from the log.

## 20. Out of scope here (follow-ups)

- `deploymentMetrics` from the log (14).
- Folding logs into `AgentIdentity.status` for earned autonomy (that spec).
- The StepTemplate custom steps running as the Pipeline's SA (that spec, using 9.1).
- Per-Pipeline cloud workload identity recipes (IRSA, GKE and Azure Workload Identity) for the
  Pipeline's SA: documentation once the identity ships.
- Making per-Pipeline identities the default.
