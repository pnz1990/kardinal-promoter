# Design: Agent budgets and blast radius

> Status: Proposed (spec for v0.14.0, AGENTIC-PLAN §5). Implementation waits until v0.13.0 is
> tagged, and follows in separate PRs (section 20). Audited against v0.13 as built on 2026-10-21
> (section 0): the findings are fixed in place below.
> Created: 2026-10-15
> Depends on: [17-actors.md](17-actors.md) (`AgentIdentity`, the verified `spec.actor` stamp on
> Bundles, the one-party rule) and [18-actor-policy.md](18-actor-policy.md) (`agent.*` read by the
> PolicyGate reconciler, §2.2; the freeze hold and its exemptions, §9).
> Verified against: this repository at `8f5d86e` (`pkg/graph/compact.go`,
> `pkg/reconciler/promotionstep`, `pkg/reconciler/bundle`, `pkg/reconciler/policygate`,
> `pkg/shard`, `api/v1alpha1`), kro v0.10.0-rc.0 (`54a203b`) and
> Kubernetes `k8s.io/apiserver` v0.37.1 (the version in `go.mod`).
> Source references are `path:line` in those trees. Re-verified on 2026-10-21 against `main` at
> `600a75656` (v0.13 as built: the actors, actor-aware policy and shards are on main) and #2372's
> compact agent index (v0.14, held): the references the audit touched are `path:line` at
> `600a75656`, and the kro pin is unchanged (`hack/install-kro.sh:42`).

---

## 0. Audit against v0.13 as built (2026-10-21)

This spec was written at `8f5d86e`, before the actors (17), actor-aware policy (18) and
namespace shards were on main. It was audited against `main` at `600a75656`, and against the
compact agent index of #2372 (v0.14, held for the v0.13 tag). The findings, and where each is
fixed:

| # | As built | What was wrong or thin here | Fix | Section |
|---|----------|-----------------------------|-----|---------|
| A1 | Each shard is its own Helm release, with its own `--agent-index` and release name (`docs/sharding.md`, the `kardinal-payments` example); the agent index reconciler is not shard-gated, so every shard builds its own index (`cmd/kardinal-controller/main.go:784-792`) | The ledger was keyed by release and written only by the default shard, so a non-default shard's steps would read an entry nobody writes and wait forever | One **ledger** per sharded installation, named by `--agent-budget-ledger` (chart `actors.budgets.ledger`, default the release name); every shard of one installation sets it to the default shard's release. Only the controller that owns cluster-scoped kinds *and* whose release is the ledger runs the budget reconciler; the others read | 15.2 |
| A2 | The agent index reconciler watches AgentIdentities with no predicate (`pkg/reconciler/agentidentity/reconciler.go:388-401`): any AgentIdentity write rebuilds the index | Section 14 required a predicate but no work item owned it. At v0.14's 7,000 AgentIdentities, each `status.usage` write would re-run `Evaluate` over the whole registry | W0: that watch passes creates, deletes and spec generation changes only. A scenario counts index reconciles per usage write (zero) | 14, 20 |
| A3 | The PolicyGate reconciler watches the **AgentIndex**, not AgentIdentities (`pkg/reconciler/policygate/reconciler.go:1481-1483`); `agent.*` reads the AgentIdentity through the cache only when a gate reads it (`actor_context.go:67-100`) | Section 10 assumed an AgentIdentity watch to add a predicate to | W5 adds an AgentIdentity watch, with a predicate on this ledger's `status.usage`, mapped to the agent's in-flight gate instances that read `budget` | 10 |
| A4 | The agent shorthand compiles to `kardinal-agents-<ceiling>` templates (`pkg/graph/agents.go:22-31`) | Named `kardinal-agents` | Renamed | 7.2, 13 |
| A5 | The controller has four field indexes: Bundles by Pipeline (`pkg/lifecycle/index.go:46`), PolicyGates, ChangeRequests by ticket, Pipelines | The spec read through indexes that do not exist | Listed as new indexes in W2, W3 and W5: PromotionSteps by `status.budget.agent`; Bundles by `spec.actor.agent` and by UID; AgentIdentities with an unavailable grant | 5.2, 7.3, 10, 20 |
| A6 | The index reconciler patches each AgentIdentity's `status.conditions` (Ready) with a JSON merge patch, which replaces the whole list (`writeStatuses`, `pkg/reconciler/agentidentity/reconciler.go:57-90`) | 7.3 said the budget reconciler writes "status.conditions entries of its own type", but 4.2 puts them under `usage` | Budget conditions live only in `status.usage.<ledger>.conditions`; the budget reconciler never writes `status.conditions` | 4.2, 7.3 |
| A7 | One AgentIdentity can carry several ledgers (separate installations) | Each entry was capped at 2,000 grants, with no bound across entries; four ledgers could approach etcd's 1.5 MiB | At most 3,000 grants per AgentIdentity across ledgers (about 600 KB); the ledger that would pass it is `LedgerFull`. The schema holds the same line against any writer (W1): at most 16 ledgers, and 3,000 grants between them | 14, 15.2 |
| A8 | v0.14's compact index (#2372): one string per entry, a 1 MiB bound, `IndexFull` refuses every checked write; supported up to 7,000 AgentIdentities | Thin: how budgets meet the index and its bound | New 14.1. Budgets add no bytes to the index, never write a `blocked` entry, and read the AgentIdentity, not the index | 14.1 |
| A9 | Supported registry size: 4,000 AgentIdentities (v0.13), 7,000 with #2372 | "2,000 AgentIdentities (the actors scale target)" | The scale case runs the release gate's 7,500 | 14, 19 |
| A10 | — | Thin: no stated default, and no release-wide switch | Off by default, at two levels: a release switch `actors.budgets.enabled: false`, and per agent (no `spec.budgets`, no effect) | 17.4 |
| A11 | Fleet pacing resolves a percentage `maxUnavailable` against the fleet's targets (`pkg/graph/fleet.go:144-160`) | Thin: no "share of a fleet" or "share of prod" limit | L5 `fleetShare`, folded and granted per fleet; a share of prod across Pipelines is decided against (absolute counts instead) | 4.1, 8.1 |
| A12 | Line drift: `handlePending` `promotionstep/reconciler.go:906-936`; fleet pacing `pkg/graph/compact.go:198-206`, `350-365`, `479`; `LabelBundleUID` `compact.go:61`; the uncached count `pkg/reconciler/bundle/reconciler.go:1480`; the graph-objects policy `identity-admission.yaml:542-575`; the cache `manager_options.go:158-194`; `buildContext` `policygate/reconciler.go:555`, `changewindow` read `:661`, `instanceGateRequests` `:1505`, `newEvaluator` `cel_evaluator.go:107` | References at `8f5d86e` | Updated where cited | throughout |
| A13 | Suites that exist: `test/e2e/journeys_test.go`, `test/scenario/enterprise/agents/{load,scale}` (`agentcases`), the in-process day (`daycases`), the live `core`, `scale` and `upgrade-agents` (#2372) suites; planned: the `soak` suite (#2403) | The test plan named no scenario or soak suite, and #2116's waiting scenario rows were not linked | 19 names the suites and adds #2116's rows and the soak's episode and invariants | 19 |

The design itself holds: the ledger on `AgentIdentity.status`, the step hold with a single-writer
grant (7), and the fold without a `ref` (9) were checked against the code as built and stand.

## 1. Problem and scope

An agent with `autonomy: auto` (17-actors §10.4) may promote as often and as widely as its scope
allows. Its sponsor answers for it, but nothing limits how much it changes before a person
notices. A wrong fix shipped by one agent to 200 Pipelines' prod environments at once is one
incident in 200 places.

This spec adds **budgets**: limits, set by cluster admins on the `AgentIdentity`, on how much an
agent's versions may change, and the **blast radius** of a bad one:

| # | Limit | Field | Counts |
|---|-------|-------|--------|
| L1 | Versions per day per environment | `versionsPerDay` | the agent's versions started into the rule's environments in any rolling 24 hours |
| L2 | Concurrent promotions | `maxConcurrent` | the agent's promotions in flight at once in the rule's environments |
| L3 | Fleet targets per hour | `fleetTargetsPerHour` | fleet targets of the agent's versions started in any rolling hour |
| L4 | Unavailable targets | `maxUnavailable` | environments and fleet targets where the agent's version failed and nothing replaced it yet |
| L5 | Share of a fleet | `fleetShare` | the share of one fleet's targets that one of the agent's versions holds before they are Verified (added by the 2026-10-21 audit, section 8.1) |

Each limit is counted across every namespace and Pipeline the agent works in, not per Pipeline:
the budget bounds the agent, not one team's use of it.

In scope: the API (section 4), what is counted and from which source (5), how windows roll over
without a clock in a gate (6), where the limit is enforced and why that is race-free (7), how it
combines with fleet pacing (8), the Graph's access (9), CEL `budget.*` (10), exhaustion and the
human override (11), admission and RBAC (12), failure modes, scale and tenancy (13-15), upgrade
(17), decisions (18), tests (19) and the work breakdown (20).

Non-goals:

- **Limits for people and bots.** Budgets apply only to versions whose stamp has `kind: agent`.
  A person's version, a bot's and the controller's own (auto-rollback, Subscription) are never
  counted or held.
- **Limiting Bundle creation.** An agent may create as many Bundles as admission allows; a
  newer one supersedes the older. What is limited is what reaches environments. A
  ValidatingAdmissionPolicy cannot count objects (17-actors §5.1), so a create-rate limit would
  need a webhook; not planned.
- **Earned autonomy** (budgets that grow or shrink with the agent's track record) and the change
  risk score are separate v0.14 items. They will read the same `status.usage`.
- **Per-tenant sub-budgets inside one agent.** A rule can be limited to namespaces (section 4.1),
  and an organisation that wants isolation registers one AgentIdentity per tenant (section 15).

## 2. Terms

| Term | Meaning |
|------|---------|
| **Rule** | One entry of `spec.budgets`: a name, the environments (and optionally namespaces) it matches, and up to four limits |
| **Request** | A PromotionStep of an agent's version that is ready to start and needs room in every rule that matches it. It is recorded in the step's own `status.budget` |
| **Grant** | The budget reconciler's decision that a request fits. It is an entry in the AgentIdentity's `status.usage.<ledger>.grants` |
| **Ledger** | The list of grants. Usage is counted from it, not from live objects |
| **Window** | The rolling period a rate limit counts over: 24 hours for L1, one hour for L3 |
| **Release** | One kardinal installation, named by its `--agent-index` (the release name, 17-actors §5.4). A sharded cluster has one release per shard (`docs/sharding.md`) |
| **Ledger** (name) | The key of `status.usage`: `--agent-budget-ledger`, by default the release name. Every shard of one sharded installation uses the default shard's release name, so they share one ledger (15.2) |

## 3. Graph-first

AGENTS.md asks three questions in order. The answers per piece:

| Piece | Question | Mechanism | Ledger |
|-------|----------|-----------|--------|
| Usage, windows and grants | (2) owned node? No: the AgentIdentity is not a Graph node, and one agent's usage spans every Graph of every namespace | A reconciler that writes its own CRD status: the budget reconciler writes `AgentIdentity.status.usage` | G8 new row; [G21](16-graph-capability-ledger.md#g21-two-graphs-cannot-claim-from-one-shared-budget), [G22](16-graph-capability-ledger.md#g22-a-graph-cannot-count-across-namespaces-without-reading-every-namespace) |
| The hold | (2) the step holds itself | The PromotionStep reconciler holds a Pending step until it finds its grant (section 7), as it holds a paused or frozen step | G8 "Holding an existing PromotionStep" row gains budgets |
| Fleet pacing | (1)/(2) data in the Graph | The translator folds the agent's `maxConcurrent` and `maxUnavailable` into each fleet target's DAG entry (`pkg/graph/compact.go`), so the existing wave filter applies them (section 8) | none |
| `budget.*` | (3) CEL variable | The PolicyGate reconciler reads `status.usage`, as it reads `agent.*` (18-actor-policy §2.2) | G8 cross-CRD reads row gains `budget.*` |
| Exemptions | VAP | `<release>-agent-budgets` verifies who adds an exemption (section 12) | none |

### 3.1 What kro does here (verified)

| Fact | Source (kro v0.10.0-rc.0) |
|------|---------------------------|
| A scalar `ref` is read with one GET when the Graph reconciles; the value is a snapshot of that moment | `pkg/graphengine/executor/simple.go:1209-1216` |
| The Graph controller reconciles several Graphs at once: `MaxConcurrentReconciles` is passed to controller-runtime. kardinal installs kro with 8 Graph workers | `pkg/controller/graph/controller.go:782-786`; `hack/install-kro.sh:53` (this repo) |
| A scalar `ref` routes every watch event of its object to every Graph that reads it, with no field or generation filter | `pkg/watch/coordinator/coordinator.go:198-235` ([G20](16-graph-capability-ledger.md#g20-a-ref-node-wakes-on-every-write-of-the-object-it-reads)) |
| A namespaced Graph may `ref` a cluster-scoped object, read as the Graph's impersonated ServiceAccount; the drift watch check drops the name | [G18](16-graph-capability-ledger.md#g18-the-ref-watch-check-ignores-the-refs-name), [G19](16-graph-capability-ledger.md#g19-a-namespaced-graph-identity-needs-a-cluster-wide-grant-to-read-a-cluster-scoped-ref); `pkg/controller/graph/impersonation.go:203-221` |
| A selector `ref` with an empty namespace lists across all namespaces, as the Graph identity | `simple.go:1228-1233`, `1263-1266`, `1288-1292` |
| `readyWhen` does not hold dependents in a standalone Graph | [G1](16-graph-capability-ledger.md#g1-readywhen-does-not-gate-dependents-in-a-standalone-graph); `simple.go:65-69`, `cmd/controller/graphengine.go:77` |
| kro never writes to a `ref` target: a ref is read-only | `simple.go:1220-1227` (the selector ref comment; the scalar path at `1180-1217` only reads) |

The consequence, used in sections 7 and 9: a Graph can evaluate "is there room" on a snapshot,
but it cannot **take** the room. Two Graphs reconciled at once, or one after the other before
anyone records the first admission, both see the same remaining count. That is
[G21](16-graph-capability-ledger.md#g21-two-graphs-cannot-claim-from-one-shared-budget).

## 4. API

### 4.1 Spec

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
metadata:
  name: fixer
spec:
  # ... subjects, sponsor, scope, autonomy, suspended (17-actors §3)
  budgets:                         # at most 16 rules; every rule that matches a step applies
    - name: prod                   # DNS label, unique in the list
      environments: ["prod", "prod-*"]   # exact names or a prefix ending in "*"
      namespaces: ["*"]            # optional, same form; default every namespace
      versionsPerDay: 5            # L1, 1..500
      maxConcurrent: 2             # L2, 1..100
      fleetTargetsPerHour: 40      # L3, 1..1000
      maxUnavailable: 1            # L4, 1..100
      fleetShare: "25%"            # L5, "1%".."100%": of one fleet's targets, per version (8.1)
    - name: pre-prod
      environments: ["uat", "staging"]
      versionsPerDay: 50
  budgetExemptions:                # break-glass, at most 32 (section 11.3)
    - name: inc-4211
      namespace: payments
      pipeline: checkout
      environment: prod            # an environment or a fleet environment
      bundle: checkout-7f3c2a      # optional; "" = any version of the agent
      reason: "hotfix for INC-4211"
      expiresAt: "2026-10-15T12:00:00Z"
      createdBy: bob@example.com   # verified by admission
      actor: {kind: human, principal: bob@example.com}   # the actor stamp, as on freeze exemptions; an agent's is never honoured (11.3)
```

**Matching.** A rule matches a PromotionStep when its namespace matches `namespaces` and its
environment matches `environments`. A fleet target matches through either its own environment
name (`prod-eu`) or its fleet environment (`prod`, the `kardinal.io/fleet` label value,
`pkg/graph/fleet.go:22`). Matching uses `actor.Matches`, the same pattern form as
`scope` (17-actors §3).

**What each limit counts.**

- `versionsPerDay`: a version counts once per (Bundle, environment), where the environment of a
  fleet target is its fleet. Rolling out one version to 40 targets of `prod` is one version.
- `maxConcurrent`: granted steps that have not settled. Settled means Verified, Superseded,
  Failed, AbortedByAlarm, RollingBack, or deleted. A failed step stops counting here and starts
  counting in `maxUnavailable`.
- `fleetTargetsPerHour`: granted steps that are fleet targets. Plain environments do not count.
- `maxUnavailable`: granted steps that failed (Failed, AbortedByAlarm or RollingBack, the
  states the Graph counts as failures, `compact.go:479`) while the Pipeline's
  `status.environments` entry for that environment still names this Bundle. A newer version in
  that environment, from anyone, or the Pipeline's deletion, ends it. Fleet targets have one
  entry each (`pkg/reconciler/pipeline/environments.go:32-45`).
- `fleetShare`: per version and fleet, the granted targets of that fleet that are not Verified or
  Superseded (a failed target keeps its place, as fleet pacing counts it), as a share of the
  fleet's targets, rounded up and at least one target (`graph.MaxUnavailableCount`,
  `pkg/graph/fleet.go:144-160`). It applies only to fleet targets.

A limit that is not set does not apply. A rule with no limit set is refused by the schema.

**Validation (CRD schema and CEL rules).**

- Rule names unique (`self.all(r, self.exists_one(s, s.name == r.name))`), at most 16 rules, at
  most 64 entries in each pattern list, `*` only at the end of an entry.
- At least one of the five limits per rule. `fleetShare` is a percentage string from `1%` to
  `100%` (pattern `^([1-9][0-9]?|100)%$`).
- The sum over all rules of the four counted limits is at most 1,500
  (`self.map(r, ...).sum() <= 1500`; `sum` is in the Kubernetes CEL lists library,
  `k8s.io/apiserver/pkg/cel/library/lists.go:145`). This bounds the ledger (section 14).
- Exemption names unique; `namespace`, `pipeline`, `environment`, `reason` (at most 256
  characters), `expiresAt` and `createdBy` required.

### 4.2 Status

```yaml
status:
  ready: true                       # 17-actors §3, unchanged
  usage:
    kardinal-promoter:              # one entry per ledger (section 15.2)
      observedGeneration: 7         # the spec generation this entry was computed for
      evaluatedAt: "2026-10-15T10:02:11Z"     # the budget reconciler's clock when it wrote this
      nextEvaluationAt: "2026-10-15T10:41:00Z" # when it requeues itself (section 6)
      rules:
        - name: prod
          environments: ["prod", "prod-*"]    # copied from the spec, so readers need only status
          namespaces: ["*"]
          versions:     {limit: 5,  used: 5, windowStart: "2026-10-14T10:02:11Z", freesAt: "2026-10-15T10:41:00Z"}
          concurrent:   {limit: 2,  used: 1}
          fleetTargets: {limit: 40, used: 12, windowStart: "2026-10-15T09:02:11Z", freesAt: "2026-10-15T09:14:30Z"}
          unavailable:  {limit: 1,  used: 0}
          fleetShare: "25%"         # copied from the spec; counted per fleet at the grant (8.1)
          waiting: 3                # requests not granted
          exhausted: [versionsPerDay]   # the limits with no room left
      grants:                       # the ledger: at most 2,000 entries (section 14)
        - bundleUID: 6b1f...        # no namespace, Pipeline or Bundle name (section 15.1)
          environment: prod-eu
          fleet: prod
          rules: [prod]
          grantedAt: "2026-10-15T09:14:30Z"
          version: false            # counted in versionsPerDay (the first grant of this Bundle into prod)
          settled: false
          unavailable: false
          exemption: ""             # the exemption it used, if any
          seeded: false             # recorded from a step that started before the ledger existed (17.2)
      exemptions:                   # the spec's exemptions as this reconciler saw them (11.3)
        - {name: inc-4211, firstSeenAt: "2026-10-15T09:00:04Z", expiresAt: "2026-10-15T12:00:00Z"}
        - {name: by-bot, firstSeenAt: "2026-10-15T09:30:00Z", expiresAt: "2026-10-15T09:30:00Z", ignored: MadeByAgent}
      conditions: [...]             # Current; LedgerFull (only here: never in status.conditions, A6)
```

**PromotionStep status** gains `budget`, written only by the PromotionStep reconciler:

```yaml
status:
  budget:
    agent: fixer
    state: Waiting                  # Waiting | Granted
    since: "2026-10-15T10:01:58Z"   # when it started waiting: the request's place in the queue
    rules: [prod]
    exemption: ""                   # set when the grant used one
```

### 4.3 Go types (sketch)

```go
// In api/v1alpha1/actor_types.go, next to AgentIdentitySpec.

type AgentBudgetRule struct {
	Name                string   `json:"name"`
	Environments        []string `json:"environments"`
	Namespaces          []string `json:"namespaces,omitempty"`
	VersionsPerDay      int32    `json:"versionsPerDay,omitempty"`
	MaxConcurrent       int32    `json:"maxConcurrent,omitempty"`
	FleetTargetsPerHour int32    `json:"fleetTargetsPerHour,omitempty"`
	MaxUnavailable      int32    `json:"maxUnavailable,omitempty"`
	FleetShare          string   `json:"fleetShare,omitempty"` // "1%".."100%" (8.1)
}

type AgentBudgetExemption struct {
	Name, Namespace, Pipeline, Environment string
	Bundle                                 string `json:"bundle,omitempty"`
	Reason                                 string
	ExpiresAt                              metav1.Time
	CreatedBy                              string
	Actor                                  *Actor `json:"actor,omitempty"` // verified by <release>-actor (11.3)
}

// AgentIdentitySpec gains:
//   Budgets          []AgentBudgetRule      `json:"budgets,omitempty"`
//   BudgetExemptions []AgentBudgetExemption `json:"budgetExemptions,omitempty"`

type AgentBudgetCount struct {
	Limit       int32        `json:"limit"`
	Used        int32        `json:"used"`
	WindowStart *metav1.Time `json:"windowStart,omitempty"` // rate limits only
	FreesAt     *metav1.Time `json:"freesAt,omitempty"`     // rate limits only: when used next drops
}

type AgentBudgetRuleUsage struct {
	Name                     string
	Environments, Namespaces []string
	Versions, Concurrent, FleetTargets, Unavailable *AgentBudgetCount // nil: limit not set
	FleetShare               string // copied from the spec
	Waiting                  int32
	Exhausted                []string
}

type AgentBudgetGrant struct {
	BundleUID   types.UID
	Environment string
	Fleet       string `json:"fleet,omitempty"`
	Rules       []string
	GrantedAt   metav1.Time
	Version, Settled, Unavailable, Seeded bool
	Exemption   string `json:"exemption,omitempty"`
}

type AgentUsage struct {
	ObservedGeneration int64
	EvaluatedAt        metav1.Time
	NextEvaluationAt   *metav1.Time
	Rules              []AgentBudgetRuleUsage
	Grants             []AgentBudgetGrant
	Exemptions         []AgentBudgetExemptionState
	Conditions         []metav1.Condition
}

// AgentIdentityStatus gains:
//   Usage map[string]AgentUsage `json:"usage,omitempty"` // key: the ledger name (15.2);
//   at most 16 ledgers, and 3,000 grants between them (14)

// PromotionStepStatus gains:
//   Budget *PromotionStepBudget `json:"budget,omitempty"`
```

## 5. What is counted, and from which source

### 5.1 Options

The question: an agent works in many namespaces. Where does the count of its promotions come
from, so that it is right, cannot be lowered by the agent or a tenant, and survives restarts?

**(a) Count live objects at each reconcile.** List the agent's Bundles (field index on
`spec.actor.agent`) and their PromotionSteps (label `kardinal.io/bundle`) across namespaces, and
count steps by state and `status.startedAt`.
- Pro: no stored state; always consistent with the cluster.
- Con: **deleting objects refunds budget.** A finished Bundle's Graph is retired and kro deletes
  its PromotionSteps (`pkg/reconciler/bundle/retire.go:186-187`); `historyLimit` prunes Bundles;
  a tenant may delete a step (the documented retry; the `<release>-graph-objects` policy does
  not check DELETE, `chart/kardinal-promoter/templates/identity-admission.yaml:549-556`) or a
  Bundle. Each deletion lowers the count inside the window. It also gives no serialisation: the
  count is a snapshot (section 7).

**(b) Count Bundle evidence.** `Bundle.status.environments` survives Graph retirement.
- Pro: survives retirement.
- Con: Bundles are still pruned and deletable; same snapshot problem as (a).

**(c) A ledger in `AgentIdentity.status.usage`.** The budget reconciler records each grant when
it makes it. Counts come from the ledger; live objects only say which requests wait and which
grants settled.
- Pro: deletion refunds nothing (a grant leaves the ledger only when no window needs it, 6.2);
  the writer that counts is the writer that admits, so the count and the admission are one
  decision (section 7); it is the record `kardinal budget` shows.
- Con: stored state that must be bounded (section 14) and rebuilt for steps that started
  before it existed (seeding, 17.2).

**(d) AuditEvents** (`PromotionStarted` with the actor).
- Con: written through per-step outboxes after the fact, so not a serialisation point; deleted by
  retention (`--audit-retention-max-age`); creation is not restricted to one writer per agent.

### 5.2 Decision: (c), the ledger

It is the only option that a deletion cannot lower and that serialises admission. Sources are
read only to find requests and to settle grants:

| Read | Why | How |
|------|-----|-----|
| PromotionSteps with `status.budget.agent == <agent>` | requests (Waiting) and their order | cache field index on `status.budget.agent` (new, A5) |
| The step of a grant (`kardinal.io/bundle-uid` and `kardinal.io/environment` labels, `LabelBundleUID`, `pkg/graph/compact.go:61`) | settled or not, failed or not | cache, by labels |
| Bundles by UID | namespace and Pipeline of a grant, for the unavailability check | cache field index on `metadata.uid` (new: the controller indexes only Bundles by Pipeline today, `pkg/lifecycle/index.go:46`, A5) |
| Pipelines | `status.environments[].bundle` for L4 recovery | cache |

A PromotionStep is created and written only by kro and the controller
(`<release>-graph-objects`, `identity-admission.yaml:542-575`), so `status.budget` cannot be
forged by a tenant. The agent is taken from the Bundle's `spec.actor`, which admission verified
and the CRD makes immutable (17-actors §4.1), never from a label.

## 6. Windows without a clock in a gate

### 6.1 Options

| | Window | For | Against |
|-|--------|-----|---------|
| (a) | Tumbling, aligned to UTC days and hours | Easy to explain ("5 a day") | Twice the limit across a boundary (5 at 23:59, 5 at 00:01); the boundary is arbitrary for teams outside UTC |
| (b) | Rolling: count grants with `grantedAt` in the last 24 h (L1) or 1 h (L3) at the moment of evaluation | No boundary burst; the meaning of "per day" a reviewer expects | The count drops at irregular times; the reconciler must requeue at each drop |
| (c) | Token bucket (refill rate, burst) | Smooth | Two parameters per limit; the state (tokens, last refill) is harder to audit than a list of grants |

**Choice: (b), rolling.** The ledger already holds each grant's time, so a rolling count costs
nothing more than a tumbling one, and `freesAt` answers "when may it ship again".

### 6.2 How a window rolls over

Only the budget reconciler reads the clock, and only to write status:

1. At each reconcile it takes `now` once and writes it as `evaluatedAt`.
2. For each rule it counts the grants with `grantedAt > now - 24h` (versions) and
   `grantedAt > now - 1h` (fleet targets), and writes `windowStart = now - window` and
   `freesAt = (oldest counted grantedAt) + window`.
3. It writes `nextEvaluationAt`: the earliest `freesAt` of a rule that has a waiting request, or,
   with nothing waiting, the earliest `freesAt` but not sooner than `evaluatedAt + 5m`. It
   returns `RequeueAfter` until then.
4. It drops a grant when no window and no state needs it: settled, not unavailable, and older
   than every window of every rule it counted in (or its rules were removed).

Every consumer compares stored fields only:

- A step proceeds when its grant is in `grants` (section 7). No time comparison.
- A gate reads `budget.env.versions.used` and `limit` (section 10). No time comparison.
- `freesAt` and `evaluatedAt` are shown in messages; nothing compares them with the current time.

**Staleness is conservative.** Only the reconciler adds grants, so a stored `used` is never
lower than the true count; it is higher only between a drop and the next write (at most 5
minutes when nothing waits, immediately when something does). If the controller is down, counts
stop dropping and nothing new is granted: limited steps hold, which fails closed.

**Clock.** One writer per agent and ledger (the leader of the ledger's controller, 15.2) reads the clock,
so windows never mix clocks except across a leader change; skew of seconds is small against an
hour. The step's `status.budget.since` is the step reconciler's clock and is used only to order
the queue.

## 7. Enforcement: the step holds, the reconciler grants

### 7.1 Options

The question: two of an agent's versions become ready for prod at the same moment with one
`maxConcurrent` place left, in two namespaces, perhaps in two controller shards. Which mechanism
lets exactly one start?

| | Mechanism | Race | Against |
|-|-----------|------|---------|
| (a) | A PolicyGate per environment with `expression: budget.env.concurrent.used < budget.env.concurrent.limit` | Both gates evaluate the same stored count and both turn ready; both steps are created | A gate is evaluated per Bundle, with no shared lock. Useful for soft policy (section 10), not for a limit |
| (b) | A ValidatingAdmissionPolicy on PromotionStep CREATE, with the AgentIdentity as params | Params come from an informer (`policy_dispatcher.go:345-354`, 17-actors §5.1): both creates see the same count | Admission cannot count objects or record a reservation. A denied create also fails kro's apply of the whole collection (G11), which is a failure, not a hold |
| (c) | The Graph's wave reads the AgentIdentity through a `ref` and admits a step only while room is left | Two Graphs reconcile in parallel (8 workers) and each reads the ref with its own GET (3.1). Even with one worker, the second Graph reads before the budget reconciler records the first admission | kro cannot write the claim back (refs are read-only). [G21](16-graph-capability-ledger.md#g21-two-graphs-cannot-claim-from-one-shared-budget) |
| (d) | The step is the request; one writer per agent grants requests in order and records each grant in the ledger; the step holds in Pending until it finds its grant | None: grants for one agent are decided one reconcile at a time, and each write is conditional on the last one (7.3) | One more hold in `handlePending`; logic outside the Graph (G8) |

**Choice: (d).** It is the only option without a double admission, and it reuses the shape the
codebase already has for limits that a snapshot cannot enforce: `maxConcurrentPromotions` counts
under a per-Pipeline lock through the uncached reader
(`countPromoting`, `pkg/reconciler/bundle/reconciler.go:1480`, under `lockPipeline`, `:1373`), and the freeze hold reads state
another reconciler wrote (18-actor-policy §9.2). (d) generalises the first across namespaces by
putting the decision in one object's status instead of a process lock, because an agent's steps
may be reconciled by different shards (`pkg/shard/shard.go`) that share no memory.

**What kro `readyWhen` can enforce here:** a predicate over a snapshot of the node's own object
(G3), and in a standalone Graph not even a hold on dependents (G1). Neither is a reservation.

### 7.2 The step side: `holdForBudget`

In `handlePending` (`pkg/reconciler/promotionstep/reconciler.go:906`), after the admission wait
(`spec.admitted`, so a step the fleet wave may still prune never asks), the pause, freeze,
environment-hold and slot holds (`:925-936`), image verification, pre-hooks and the required-gate
re-check (`:948-987`),
and before the transition to Promoting:

0. With budgets off for the release (`--agent-budgets=false`, 17.4), pass.
1. Read the Bundle (already read by `holdForSlot`, `lifecycle.go:111`). If `spec.actor.kind` is
   not `agent`, pass.
2. Read `AgentIdentity/<actor.agent>` from the cache. If it has no `spec.budgets`, pass: no rule
   can hold it. If it is missing, hold with `agent fixer is not registered (AgentIdentity fixer
   not found)`: the `kardinal-agents-<ceiling>` gates (18-actor-policy §7, `pkg/graph/agents.go:22-31`)
   already block such a version,
   and this keeps a step created before deregistration from starting.
3. Read `status.usage.<ledger>`. If it is missing or its `observedGeneration` is not the spec's
   generation, hold: `waiting for agent fixer's budget to be evaluated`. A budget just added or
   tightened applies before any step passes; an edit fails closed.
4. If no rule in `usage.rules` matches the step, pass.
5. If `grants` has an entry for (this Bundle's UID, this step's environment), write
   `status.budget.state: Granted` (and `exemption`) and pass. The grant is keyed by Bundle and
   environment, not by step, so a step deleted and recreated by the Graph finds the same grant:
   it is neither charged twice nor refunded.
6. Otherwise write `status.budget: {agent, state: Waiting, since: now, rules}` once, and the
   message `waiting for agent fixer's budget (rule prod: versionsPerDay); kardinal budget fixer`.
   The message names the exhausted limit but no counts, so it does not change, and the step is
   not rewritten, while counts move. Requeue after one minute as a fallback.

The step writes only its own status. It never reads a clock to decide; `since` is a status
write.

**Waking.** The step reconciler watches AgentIdentities (a new watch: it watches none today,
`promotionstep/reconciler.go:2687-2696`). Its update handler compares the old and new `grants`
of this ledger and enqueues only the steps of new grants (found by the Bundle UID and
environment labels). A grant wakes one step; a status write that grants nothing wakes none.

**As built (W3, `pkg/reconciler/promotionstep/budget.go`).** The order of the checks differs
from the list above in three places, each toward fewer held steps and never toward an unbudgeted
start:

- The grant is looked up first (step 5 before 2-4): a grant is never revoked, so a step that has
  one goes on whatever the spec or the ledger's generation says now.
- Whether a rule applies (step 4) is decided from the spec, with the reconciler's `Matching`,
  not from `usage.rules`: a rule just added holds the step before the ledger has caught up (it
  waits on step 3), and a step no rule of the spec matches passes even while the ledger is
  behind.
- A step held on step 3 also writes `status.budget` (Waiting), so the reconciler sees the
  request at once; a waiting step that no rule matches any more (budgets removed or narrowed)
  clears it before it goes on.

Waking as built: the handler lists the agent's steps through the `status.budget.agent` index
(registered once, `agentbudget.IndexStepsByBudgetAgent`, shared with the budget reconciler) and
enqueues the Pending ones of the grants the write added, found by the `kardinal.io/bundle-uid`
label or, for a step of a non-compact Graph, its Bundle's UID; and every waiting step of the agent
when the ledger caught up with the spec, or the spec changed. A step held because its agent is
not registered has no `status.budget`, and re-checks on the one-minute fallback.

### 7.3 The reconciler side: granting

A new controller, `pkg/reconciler/agentbudget`, reconciles AgentIdentities (one work-queue key
per agent, so one agent is never reconciled twice at once). It runs only in the controller that
owns cluster-scoped kinds (`Gate.OwnsClusterScoped`, `pkg/shard/shard.go:242`) and whose release
is the ledger (15.2), only on the leader, and only with budgets on (17.4). Several workers are
safe: each works on a different agent. It writes only `status.usage.<ledger>`, conditions
included: never `status.conditions`, which the agent index reconciler owns and patches as a whole
list (A6).

Per reconcile of agent `A`:

1. `now` once. Build the rules from the spec (and copy them into status, so steps and gates read
   status only).
2. Settle grants from live state: a grant's step settled or gone sets `settled`; a failed step
   whose Pipeline environment still names its Bundle sets `unavailable`; a newer Bundle there,
   or a deleted Pipeline, clears it.
3. Count every rule (6.2).
4. Collect the requests: steps with `status.budget.agent == A` and `state: Waiting`, whose
   Bundle's `spec.actor.agent` is `A`, not already granted. Order them by `since`, then
   `creationTimestamp`, then namespace and name.
5. For each request in order: an unexpired exemption that matches it (11.3) grants it with
   `exemption` set. Otherwise it is granted when every matching rule has room for it in every set
   limit (a version grant only if this Bundle has none in that environment yet; a fleet-target
   grant only for a target; `unavailable.used < limit`). A request that does not fit **blocks the
   later requests of the same rules** (strict order, so a large fleet cannot be starved by single
   environments behind it); requests that match none of the blocked rules continue.
6. If anything changed, write the ledger's entry with a JSON merge patch carrying the
   `resourceVersion` it read (controller-runtime `MergeFromWithOptimisticLock`). The API server
   refuses a stale write with Conflict (`k8s.io/apiserver/pkg/registry/generic/registry/store.go:807-808`);
   the reconciler re-reads (uncached) and starts over. An unchanged result writes nothing.
7. `RequeueAfter` until `nextEvaluationAt`.

The conditional write is what makes the grant safe even if two writers exist for a moment (a
leader handover, or a second installation writing its own ledger of the same object): a grant computed
from a stale ledger never lands. Grants are never revoked; a step that found its grant may
rely on it.

**Triggers.** AgentIdentity spec changes (generation, including exemptions); PromotionSteps whose
`status.budget` or `status.state` changes and that carry `status.budget.agent` (mapped to that
agent); Pipeline `status.environments` changes, mapped to the agents that hold an unavailable
grant (a new cache index on AgentIdentities with any `unavailable: true` grant, A5; the ledger has no
Pipeline names, C12, so the mapping enqueues those few agents and their reconcile resolves the
Bundle UIDs); its own requeue, and a resync every 5 minutes while seeded grants are unsettled
(17.2), because a seeded step has no `status.budget` and so sends no event the mapping sees.

**As built (W2, `pkg/reconciler/agentbudget`).** The decision is one function, `evaluate`, over
the ledger read and the facts gathered from the cache; it reads no clock and no object, and is
tested on its own. Details the steps above leave open:

- A grant's `settled` is recomputed at each evaluation from its step: a step recreated by the
  Graph (a retry) is under way again and counts in `maxConcurrent` again; its grant is found by
  Bundle and environment, so it is not charged a second version.
- After a spec change, each grant's `rules` are re-derived from its Bundle's namespace and its
  environment or fleet; a grant whose Bundle is gone keeps the rules that are still defined.
- A settled, available grant is dropped once it is older than every window it counts in: 24 h
  for a version grant under a `versionsPerDay` rule, 1 h for a fleet target under a
  `fleetTargetsPerHour` rule, at once otherwise.
- A Conflict is recomputed from an uncached read up to three times, then returned (the work queue
  retries with backoff). Any other refused write, such as a 17th ledger (15.2), is a Warning
  event `BudgetLedgerWriteRefused` on the AgentIdentity.
- `LedgerFull` (reason `GrantCap`, `AgentGrantCap` or `ObjectSize`) is set, with one Warning
  event `BudgetLedgerFull`, when 2,000 grants, 3,000 across the agent's ledgers, or 1 MiB of
  AgentIdentity JSON would be passed; this evaluation's grants are dropped newest first until the
  object fits.
- Events go to the AgentIdentity, so only when the controller watches every namespace (an Event
  on a cluster-scoped object is written in `default`); in namespace mode the ledger's conditions
  and the log say the same.
- Triggers as built: AgentIdentity generation changes (not the ledger's own writes); a step with
  `status.budget` created, deleted, or changing `state` or `status.budget`; a Pipeline whose
  `status.environments` changed, mapped through the index of agents holding an unavailable grant
  in this ledger; and the reconciler's own requeue.

### 7.4 What this does not hold

Steps already past Pending when a budget is added or tightened keep running; they are counted
(seeding, 17.2). A granted step that is then paused or frozen keeps its `maxConcurrent` place
until it settles: the place is taken by a change in progress.

## 8. Fleets: combining with the fleet pacing in `compact.go`

The compact shape paces a fleet per Bundle, in the Graph:

- `maxConcurrent`: at most that many of the fleet's targets in flight (a step exists and is not
  Verified or Superseded; a Failed target keeps its place), `compact.go:350-365`.
- `maxUnavailable`: once that many of the fleet's targets failed, none starts,
  `compact.go:359-365` with `failedFleets` at `:479`.
- `paused`: `spec.pausedFleets`, `compact.go:278-288`.

These bound one version in one fleet. The agent's limits bound all of its versions in all fleets.
They combine in two places:

1. **Fold at translation (the Graph).** For a Bundle stamped `kind: agent`, the translator reads
   the AgentIdentity (from the cache, as it reads the Pipeline) and writes into each fleet
   target's DAG entry `maxConcurrent = minPositive(fleet.maxConcurrent, rule.maxConcurrent)` and
   `maxUnavailable = minPositive(resolved fleet.maxUnavailable, rule.maxUnavailable)` over the
   rules that match the fleet (`compactStep.maxConcurrent` and `.maxUnavailable`,
   `compact.go:198-206`, `283`). With `fleetShare` set, `maxConcurrent` also takes
   `MaxUnavailableCount(fleetShare, targets)` (8.1). The existing wave filter then creates fewer
   steps. An agent-wide
   bound of N implies a per-fleet bound of N, so the fold never admits what the grant would
   refuse; it only keeps steps from being created just to wait.
2. **Grant at the step.** The ledger check (7.3) enforces the agent-wide count across fleets,
   Pipelines and namespaces, and `fleetTargetsPerHour`, which the Graph cannot (it has no clock,
   G8).

The fold is a snapshot of the AgentIdentity at translation. A **tighter** budget applies at once
through the grant; a **looser** one reaches an in-flight Graph only when it is rebuilt. The
direction of staleness is the safe one, so AgentIdentity edits do not rebuild Graphs.

**As built (W4).** With `--agent-budgets`, the translator reads the AgentIdentity of a version
stamped `kind: agent` from the cache (`Translator.WithAgentBudgets`) and passes its rules as
`BuildInput.AgentBudgets`; `graph.foldBudgets` lowers each fleet target's `maxConcurrent` and
`maxUnavailable` (as resolved from the fleet's spec) by every rule that applies to the target,
decided with `actor.BudgetRuleApplies`, the function the budget reconciler and the step hold use.
A person's version, an unregistered agent and budgets off pass no rules, and their Graphs are
byte for byte unchanged. `fleetShare` resolves against the fleet's targets at translation; the
grant re-checks it against the fleet's current size (8.1).

**Held targets and fleet places.** A target held for budget is a step that exists and is not
settled, so it keeps its fleet place, exactly as the fleet's `maxConcurrent` means: no target
behind it starts. Waiting steps per fleet are therefore at most the folded `maxConcurrent`. A
fleet with `maxConcurrent: 0` (every target at once) and only `fleetTargetsPerHour` set creates
every target's step and holds most of them; the docs recommend setting `maxConcurrent` with
`fleetTargetsPerHour`.

**Pacing presets.** The AWS-style wave presets planned for v0.14 (1, 1, 3, 12, rest) produce
fleet entries the same way; the fold applies to whatever they produce.

### 8.1 Share of a fleet, and of prod (added by the audit)

A count does not say how much of a fleet one change reaches: 10 targets is a canary in a fleet of
500 and the whole of a fleet of 10. Two shares were asked for: of one fleet, and of prod across
Pipelines.

| | Limit | For | Against |
|-|-------|-----|---------|
| (a) | `fleetShare` per rule, per version and fleet, folded into the fleet's `maxConcurrent` and checked at the grant | The fleet already counts in-flight targets per version; the share resolves against its own size as `maxUnavailable` percentages do (`MaxUnavailableCount`, `pkg/graph/fleet.go:144-160`), so no new counting | Per version: two versions of one agent in one fleet each get the share (one Pipeline promotes one version at a time unless `maxConcurrentPromotions` is raised) |
| (b) | A share of every matching environment across the agent's Pipelines (`prod` of 200 Pipelines, at most 10% of them unsettled or failed) | One number for "how much of prod" | The denominator is every matching environment the ledger's release sees. It moves with each Pipeline added or removed, so the same budget means a different number each hour, and it counts Pipelines the agent's sponsor may not know about |
| (c) | Absolute agent-wide `maxConcurrent` and `maxUnavailable` on the prod rule (L2, L4) | Exists in this spec; an admin knows the size of prod when they set it | Not a share: it must be revisited when prod grows |

**Choice: (a) for fleets, (c) for prod.** A fleet's size is fixed in its Pipeline's spec, so a
share of it is stable and auditable; a share of "all prod" is not. The grant checks `fleetShare`
for each fleet-target request: the version's unsettled grants in that fleet, plus this one, must
not pass the resolved count. The fleet's target count comes from the Pipeline in the cache
(`graph.FleetTargetEnvironments`), so a fleet that grows between the fold and the grant uses the
newer size at the grant. (b) can be added as `prodShare` if users ask, with the denominator
written to `status.usage` so it is auditable.

## 9. The Graph's access to the budget: is a `ref` node right?

AGENTIC-PLAN §5 proposed that the Graph reads the budget through a `ref` node on the
AgentIdentity. Checked against kro before building on it:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A scalar `ref` on `AgentIdentity/<agent>` in every agent Bundle's Graph; the wave reads `${agentIdentity.status.usage...}` | Graph-native; the Graph would skip creating steps that would only wait | It cannot enforce the limit (7.1 (c), G21). It needs a cluster-wide grant for every agent namespace's Graph identity (G19) and a whole-kind watch (G18). Each status write wakes every Graph that reads it (G20): an agent with 200 Graphs in flight and two writes per promotion costs 400 Graph walks per promotion, each about three API calls per object (G9), in kro's one queue (G13) |
| (b) | A namespaced mirror (`AgentBudgetView` per agent and namespace) written by the controller, read by a namespaced `ref` | No cluster-wide grant | One write per namespace per usage change (fan-out moves to etcd); the same G20 wake-ups; a new kind; still cannot enforce |
| (c) | No runtime ref. The translator folds the limits the Graph can apply (8.1) at translation; the step and the gate read status through their reconcilers | No Graph RBAC, no wake-ups, no new kind; the Graph still creates no step beyond the folded limits | The Graph does not see live usage; a looser budget reaches in-flight Graphs only on rebuild |

**Choice: (c).** A ref here would add cost and RBAC widening for an optimisation the fold
already provides, and could not replace the grant. It matches 18-actor-policy §2.2 (B1), which
moved `agent.*` off a ref for the same reasons. (a) becomes worth revisiting only if kro fixes
G18, G19 and G20 (a field-filtered wake) and the grant still stays where it is.

**The cluster-scoped ref from a namespaced Graph** (the question G19 records) is therefore not
needed for budgets. If (a) is adopted later, the shape is the one 17-actors §10.2 described: one
chart ClusterRoleBinding whose subjects the IdentityProvisioner patches.

## 10. CEL: `budget.*`

The PolicyGate reconciler adds `budget` to the context it builds in `buildContext`
(`pkg/reconciler/policygate/reconciler.go:555`) and declares it in `newEvaluator`
(`cel_evaluator.go:107`) as `DynType`, read only when the expression or
`approval.requiredWhen` contains `budget` (as `changewindow` is, `reconciler.go:661`):

| Variable | Type | Value |
|----------|------|-------|
| `budget.applies` | bool | the version is an agent's, and a rule of its AgentIdentity matches the gate's environment (`kardinal.io/environment` label of the instance) |
| `budget.agent` | string | `actor.agent`; `""` |
| `budget.current` | bool | `status.usage.<ledger>.observedGeneration` equals the spec generation |
| `budget.env.rule` | string | the first matching rule in spec order; `""` |
| `budget.env.versions`, `.concurrent`, `.fleetTargets`, `.unavailable` | map | `{set, limit, used, remaining}`; `set: false` and zeros when the limit is not set |
| `budget.env.fleetShare` | string | the rule's `fleetShare`; `""` when not set |
| `budget.env.waiting` | int | requests waiting under the rule |
| `budget.env.exhausted` | bool | any set limit of the rule has `remaining == 0` |
| `budget.rules.<name>` | map | the same shape, for every rule of the agent |
| `budget.evaluatedAt` | string | RFC 3339, for messages only |

Every key except under `budget.rules` is always present, so expressions evaluate (to false) for
people's versions instead of failing with "no such key". `used` is the count before this version:
a gate is evaluated before the step exists and asks for room.

The values come from status only (`status.usage.<ledger>`), the same source the step uses, read
through the cache: never a live query, and nothing compares them with the clock. With budgets
off (17.4), `budget.applies` is false and every value is zero.

**Waking.** As built, the PolicyGate reconciler watches the AgentIndex, not AgentIdentities
(`reconciler.go:1481-1483`), and `agent.*` reads the AgentIdentity through the cache only when a
gate reads `agent` (`actor_context.go:67-100`). Budgets add an AgentIdentity watch with a
predicate that passes only a change of this ledger's `status.usage`, mapped to the gate
instances whose expression or `requiredWhen` contains `budget` and whose Bundle's
`spec.actor.agent` is the agent: a new Bundle field index on `spec.actor.agent` (A5), then the
instances of those Bundles (`instanceGateRequests`, `reconciler.go:1505`).

**As built (W5).** `buildBudgetContext` (`pkg/reconciler/policygate/budget_context.go`) applies
the rules with `actor.BudgetRuleApplies` to the instance's namespace, its `kardinal.io/environment`
label and its `kardinal.io/fleet` label, the decision the reconciler, the step hold and the fold
use. The AgentIdentity watch is registered only with `--agent-budgets`, through the shared
`agentbudget.IndexBundlesByAgent`. `kardinal policy simulate --as-agent` reads the ledger named for
the release's AgentIndex, else the agent's only ledger. `docs/reference/cel-context.md` documents
the keys, and its examples are evaluated against the context by `TestDocumentedCELContext`.

What gates are for here, and what they are not:

```yaml
# More scrutiny as the agent uses its day: a person approves once half the budget is spent.
approval:
  required: 1
  excludeAuthor: true
  requiredWhen: "budget.applies && budget.env.versions.set && budget.env.versions.used * 2 >= budget.env.versions.limit"
```

A gate such as `!budget.env.exhausted` shows the exhaustion as a gate, but it does **not**
enforce the limit (7.1 (a)): the grant does. The user guide says so next to the example.

`kardinal policy simulate --as-agent fixer` (18-actor-policy §2.1) reads the AgentIdentity's
current `status.usage`.

## 11. Exhaustion and the human override

### 11.1 Hold, not fail

| | Behaviour | For | Against |
|-|-----------|-----|---------|
| (a) | Fail the step (`Failed: budget exhausted`) | Loud; the Bundle stops | A budget is a rate, not a verdict on the change: failing turns waiting into a failure, triggers `onHealthFailure` paths, counts in DORA failure rates and in the agent's own `maxUnavailable` |
| (b) | Hold the step in Pending until a grant | The version ships when room returns; nothing is lost; the same model as pause and freeze | A held version can wait a day; a newer one supersedes it |
| (c) | Hold before the Graph exists (`WaitingForSlot` on the Bundle, like `maxConcurrentPromotions`) | Nothing is created | The whole Bundle waits, including environments no rule matches (test, uat) |

**Choice: (b).** The step stays Pending and the Bundle Promoting. A newer version of the same
Pipeline supersedes the waiting one as usual; a superseded request leaves the queue without using
a grant. Pending has no expiry of its own (`WaitForMergeExpiry` and `HealthCheckExpiry` start in
later states), so a hold cannot time out into a failure.

### 11.2 Where it is visible

- The step's `status.message` (7.2), which `kardinal why`, `kardinal status`, the UI stage
  drawer and PR evidence already show for held steps.
- `kardinal budget <agent>` (new): each rule's counts, windows and `freesAt`, waiting requests
  resolved to namespace, Pipeline, Bundle and environment for a caller who may read them, and
  the exemptions in effect. `-o json` prints `status.usage`.
- A Kubernetes Event on the AgentIdentity when a rule becomes exhausted or has room again
  (`BudgetExhausted`, `BudgetAvailable`), once per transition.
- The UI agent page (v0.14 analytics by actor) shows the same numbers.

**As built (W7).** The Pipeline projects the hold as `status.environments[].budgetHeldBy` (the
agent), from its newest Bundle's Pending step whose `status.budget` is Waiting, so `kardinal
status` shows "Held by agent fixer's budget" and `kardinal why` answers "prod waits for agent
fixer's budget." with the step's message, `Next: kardinal budget fixer` and `Or: kardinal
override ... --budget`. `kardinal budget` prints every ledger of the agent (or `--ledger`'s) and
lists the waiting steps only when the caller may list PromotionSteps in every namespace (it says
so otherwise). `kardinal doctor` checks the controller Deployment's flags: with budgets off it
names the AgentIdentities whose budgets are kept but not enforced; with them on it fails on a
budgeted agent with no entry in this release's ledger (no writer: a shard naming a ledger whose
release does not run the reconciler) and warns on one behind its spec. The budget reconciler
emits `BudgetExhausted` and `BudgetAvailable` (Normal) when a rule runs out of room or has room
again. The UI's agent page (analytics by actor) is not built yet; the stage drawer shows the
held step's message.

### 11.3 Human override: exemptions

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A PolicyGate override | Existing command | The budget is not a gate (section 7); anyone who may override gates in the namespace would lift a cluster-wide limit an admin set |
| (b) | `spec.budgetExemptions` on the AgentIdentity: one Pipeline environment (optionally one version), with reason, expiry and a verified `createdBy` | Only someone the cluster lets lift the agent's limit can; one record per exception; the reconciler applies it like a grant, so the step needs no new logic | A new admission policy and a custom RBAC verb |
| (c) | Edit the budget (raise the limit, then lower it) | Nothing new | Lifts the limit for every Pipeline; leaves no record of what was let through |

**Choice: (b).** The same shape as freeze exemptions (18-actor-policy §9.3, B8).

- `kardinal override <pipeline> <env> --budget --reason <why> [--bundle <name>] [--expires-in 1h]`
  adds an exemption to the AgentIdentity of the Bundle's agent, as the caller.
- The budget reconciler treats an exemption as in effect from when it first sees it until
  `min(expiresAt, firstSeenAt + 24h)` (admission has no clock, so the cap is applied here). It
  lists every spec exemption in `status.usage.<ledger>.exemptions` with that `expiresAt`, and keeps
  an expired one listed until it is removed from the spec, so the cap never restarts.
- **An exemption an agent made never counts** (break-glass is for people, as for freeze
  exemptions, `pkg/reconciler/changewindow/freeze.go` `exemptionByAgent`). The author is the
  exemption's `actor` stamp, not `createdBy`: `createdBy` holds a username only, and an agent can
  be a Group subject, which only the requester's groups reveal (`pkg/actor/actor.go` `Derive`).
  The stamp is what `<release>-actor` derives from `request.userInfo`, username and groups,
  against the agent index (12). An exemption stamped `kind: agent` is listed with
  `ignored: MadeByAgent`, and one with no stamp with `ignored: Unverified` (W6 requires a stamp on
  every new exemption, so an unstamped one was never verified). Neither grants anything. The
  ledger's `ExemptionIgnored` condition names each, and the reconciler emits one Warning event
  (`BudgetExemptionIgnored`) per exemption on the AgentIdentity, not repeated on re-runs. While
  the agent index is not Ready, what admission accepts and stamps follows #2451 (index
  readiness never refuses a person); the reconciler reads the stamp it is given.
- An exempt grant needs no room but is counted, so the record stays honest and later requests
  see the true usage.
- The step that proceeds on an exempt grant writes one `BudgetExempted` AuditEvent through its
  status outbox, named from the step and the exemption, so a re-run writes no second one. It
  records `createdBy`, reason and expiry.
- Removing an exemption ends it at once for requests not yet granted.

People's and the controller's versions are never held (section 1), so a person can always ship
the same fix themselves: `kardinal promote` or `kardinal rollback` as a person is not budgeted.
An agent's own rollback **is** budgeted: `spec.provenance.rollbackOf` is written by the client
and is not verified, so exempting rollbacks would let an agent label any version a rollback.

## 12. RBAC and admission

**Who sets budgets.** Writing `spec.budgets` is writing the AgentIdentity, which only cluster-wide
admins may (17-actors §3, RBAC).

**Who adds exemptions.** A new `<release>-agent-budgets` ValidatingAdmissionPolicy matches UPDATE
of `agentidentities`. Kubernetes 1.35 is the floor, so `authorizer` is available
(`k8s.io/apiserver/pkg/cel/environment/base.go`, 17-actors §5.1):

- Any change to the spec other than appending `budgetExemptions` entries requires
  `authorizer.group('kardinal.io').resource('agentidentities').name(object.metadata.name).check('manage').allowed()`.
  Cluster admins (`*` verbs) pass; the chart's `<release>-admin` ClusterRole gains `manage`.
- Each new exemption requires `check('exempt')` on the same object, and
  `createdBy == request.userInfo.username`.
- Each new exemption carries an `actor` stamp, checked by `<release>-actor` as freeze exemptions'
  are (W6): the stamp must say what `request.userInfo`, username and groups, is against the agent
  index, and an agent's is refused (`chart/kardinal-promoter/templates/actor-admission.yaml:140`,
  "break-glass is for people"). The budget reconciler ignores an exemption stamped as an agent's
  or not stamped (11.3), so the controller's own writes, which `<release>-actor` exempts, are held
  to the same rule. Behaviour while the index is not Ready is #2451's.
- The creator may not be of the agent's party (17-actors §10.3): not the sponsor, not a User
  subject, not a ServiceAccount subject's username, and not in a Group subject:

  ```
  object.spec.sponsor.user != request.userInfo.username &&
  !object.spec.subjects.exists(s,
     (s.kind == 'User' && s.name == request.userInfo.username) ||
     (s.kind == 'Group' && s.name in request.userInfo.groups) ||
     (s.kind == 'ServiceAccount' &&
        'system:serviceaccount:' + s.namespace + ':' + s.name == request.userInfo.username))
  ```
- Existing exemptions cannot change; removal is allowed.
- This release's controller and `admission.controllerUsernames` are exempt (the UI API writes as
  the controller for callers it authenticated, with the caller as `createdBy`).

**As built (W6, `chart/kardinal-promoter/templates/agent-budgets-admission.yaml`).**

- `<release>-agent-budgets` holds the who-checks above with the API server's authorizer
  (`manage`, `exempt`) and, needing no AgentIndex, that each new exemption's stamp names the
  requester with no `via` (a ServiceAccount's says `bot` or `agent`). Removing an exemption needs
  `exempt`, like adding one, so an incident commander can withdraw their own. The spec fields a
  `manage` check guards are listed in the template, and a test keeps the list to
  `AgentIdentitySpec`'s.
- The agent check of the stamp is `<release>-actor-budgets`: the actor policies' own rules
  (`kardinal-promoter.actorChecks`) on the new exemptions, against the AgentIndex. It is a policy
  of its own, not a rule of `<release>-actor`, because a policy's AgentIndex param is resolved
  before its match conditions and validations: matching every AgentIdentity write would refuse
  all of them, registrations included, while the index object is missing. Its binding selects by
  the label `kardinal.io/budget-exemptions: "true"`, which `<release>-agent-budgets` requires
  exactly while the identity has exemptions; only those identities' writes need the index.
- The controller counts a stamped exemption only while both policies and their bindings exist
  (`--agent-budget-exemption-policies`, `policygate.IdentityPolicyCheck` for each, as for gate
  overrides), and `--agent-budgets` refuses to start without the flag; the chart sets both.
  Otherwise the exemption is listed `ignored: Unverified` (11.3).
- `admin-extra` (aggregated into `<release>-admin`) gains `manage` and `exempt` on
  `agentidentities`.

The chart adds a ClusterRole `<release>-agent-budget-exempter` (`get, patch` and the custom verb
`exempt` on `agentidentities`) for incident commanders. Because `patch` alone lets them write the
whole object, the policy's `manage` check is what keeps them to exemptions.

**Who writes `status.usage`.** Only the controller has `update` on `agentidentities/status`
(17-actors §5.4, Writers; `chart/kardinal-promoter/templates/_rbac.tpl:261`). That is RBAC only:
no policy guards the status as `<release>-agent-index` guards the index's, so a cluster admin can
write a grant. They can also raise `spec.budgets`, so it gains them nothing; the ledger defends
against tenants and agents, not against cluster admins.

**Who writes `status.budget`** on steps: the controller, under the existing
`<release>-graph-objects` policy.

**Who reads it.** `status.usage` is readable by whoever may read AgentIdentities (by default every
authenticated user, `actors.directory.public`). It carries counts, times, Bundle UIDs and
environment names, but no namespace, Pipeline or Bundle names (15.1).

## 13. Failure modes

| Failure | Effect | Why it is safe |
|---------|--------|----------------|
| The controller (or the default shard) is down | No grants; budget-limited agent steps hold; others proceed | Fail closed; nothing is admitted from an unwritten ledger |
| A budget is added or tightened | Steps hold until `observedGeneration` catches up (one reconcile), then are granted under the new limits | A spec ahead of status holds (7.2 step 3) |
| A budget is loosened | Waiting requests are granted on the next reconcile; folded fleet limits stay stricter until a rebuild | Safe direction (section 8) |
| Two writers at once (leader handover, two releases) | One write wins; the other gets Conflict and recomputes | Optimistic lock (7.3 step 6) |
| A step is deleted and recreated | It finds its grant by Bundle and environment | No double charge, no refund |
| A Bundle or its steps are deleted or retired | Version and fleet grants stay until their window ends; `maxConcurrent` is freed; `maxUnavailable` clears only when the environment moves on | No refund within a window |
| The ledger reaches 2,000 entries, or the AgentIdentity 3,000 across ledgers (A7) | `LedgerFull` condition on that ledger; new requests wait; existing grants keep working | Fail closed; the schema bound (4.1) makes this a misconfiguration signal |
| A 17th installation tries to keep a ledger on the agent (15.2) | The API server refuses its status write; its steps for the agent hold; a Warning event and `kardinal doctor` say why | Fail closed; the other 16 ledgers are untouched |
| Budgets are turned off for a release with budgets set (17.4) | Nothing is held or granted; the ledger stays as it was; `kardinal doctor` names the AgentIdentities with budgets | An operator's decision, made visible; turning it on again seeds the ledger (17.2) |
| The agent index is full or not built | Checked writes are refused by the actor policies, budgets or not; the budget reconciler and the step hold read the AgentIdentity, not the index, and carry on | Budgets never depend on the index (14.1) |
| The AgentIdentity is deleted | Its steps that have not started hold; started ones run | 17-actors §3 requires suspension first; the `kardinal-agents-<ceiling>` gates also hold such versions |
| The agent is suspended | Admission refuses its writes (17-actors); gates see `agent.suspended`; the ledger is kept | Suspension is the kill switch; budgets are a rate |
| A request's Bundle is superseded | The step fails before it starts, as every unstarted step of a superseded version does ("superseded before this step started"); it keeps its last `status.budget` but is no request: the ledger, `kardinal budget` and `override --budget` count only steps that have not started (as built, W7) | No grant wasted |

## 14. Scale and the cost of a status write

**Writes per promotion.** One status write grants a batch of requests (the work queue coalesces
events per agent); one marks grants settled; a failure and its recovery add one each; window
drops add at most one every 5 minutes while nothing waits. About two writes per agent promotion.
At 1,000 agent promotions an hour across a cluster: about 2,000 writes an hour, 0.6 a second.
The step adds one status write when it starts waiting (the message does not change while counts
move, 7.2).

**Object size.** A grant is about 150 to 200 bytes of JSON. A ledger holds at most the grants
some window or state needs, which the schema bounds by the sum of all limits (1,500, section 4.1),
plus exempt grants, under a hard cap of 2,000 entries: about 400 KB. One AgentIdentity can carry
the ledgers of several installations (15.2), so the reconciler also caps the grants across all of
its ledgers at 3,000 (about 600 KB, well under etcd's 1.5 MiB with the spec): the ledger whose
grant would pass it is `LedgerFull`, and its requests wait (A7). The schema holds the same line
against any writer, a buggy one included: `status.usage` has at most 16 ledgers (15.2), and a CEL
rule refuses more than 3,000 grants between them (W1). Typical agents hold tens of entries.

These sizes assume short rule names and patterns. The schema's per-field bounds alone allow more:
a grant naming 16 rules of 63 characters is about 1.2 KB, and each ledger copies its rules'
patterns, up to 128 per rule. A status write past the API server's limit is refused, so the
ledger cannot grant and the agent's steps hold (fail closed). W2 therefore also marks a ledger
`LedgerFull` before the AgentIdentity's JSON would pass 1 MiB.

**Who receives each write.** Every kardinal controller replica's cache (AgentIdentities are
already cached for the AgentIndex, 17-actors §5.4); the step reconciler's handler (it wakes only
newly granted steps); the PolicyGate mapper (only instances that read `budget` for that agent).
The agent index reconciler must ignore writes that change only `status.usage`, or every
promotion would rebuild the index. As built, its AgentIdentity watch has no predicate
(`pkg/reconciler/agentidentity/reconciler.go:388-401`), so W0 adds one: creates, deletes and
spec generation changes only. It writes each AgentIdentity's `status.ready` itself, from its own
evaluation, so it needs no status events. kro receives nothing: no Graph reads the AgentIdentity
(section 9).

**Many agents.** The reconciler's work per agent is proportional to its requests and grants,
from indexed cache reads. 7,000 AgentIdentities (v0.14's supported registry, with the compact
index of #2372) with budgets cost nothing while idle: a requeue only when a count must drop.

**Many targets.** A fleet of 500 targets with the folded `maxConcurrent` of 10 creates at most 10
held steps at a time (section 8). The step wake-up is per grant, not per write.

### 14.1 Budgets and the agent index's size bound

v0.14 writes the agent index in its compact format (#2372, `docs/actors.md#the-index-format`):
one string per entry (agent, sponsor, autonomy, scope, blocked), a 1 MiB bound, about 115 bytes
an AgentIdentity, and `IndexFull` refuses every write the actor policies check. Budgets keep out
of it:

- **No bytes in the index.** Budgets, usage and exemptions live on the AgentIdentity, not in the
  index entries, so they move neither the index's size, nor `NearFull`, nor `IndexFull`, nor the
  admission cost of the actor policies, which convert the index for each checked request.
- **Exhaustion is never an index `blocked` entry.** A blocked entry makes admission refuse the
  agent's writes, at once and everywhere. A budget holds a step instead (11.1), and rewriting the
  index on every exhaustion would rewrite the admission param on every grant.
- **Usage writes do not rebuild the index** (W0, above).
- **Budgets do not depend on the index.** The budget reconciler and the step hold read the
  AgentIdentity's spec and status from the cache. A full or not-yet-built index refuses checked
  writes anyway; budgets keep working for the versions already admitted.
- **The AgentIdentity grows, the index does not.** A ledger is up to about 400 KB on its
  AgentIdentity (14). Every controller replica caches AgentIdentities already (the index is built
  from them), so the cost is memory in the controller: 50 agents with full ledgers are about
  20 MB. Agents without budgets carry no `status.usage`.
- **Sizing.** An agent counts against the index once whatever its budget; the supported 7,000
  AgentIdentities (`docs/operate.md#scale-and-limits`) are unchanged by budgets.

## 15. Tenancy and releases

### 15.1 Several tenants share one agent

The budget is the agent's: one tenant's promotions use room another tenant's would have had.
That is the meaning of a per-agent blast radius. Options considered:

| | Aggregation | For | Against |
|-|-------------|-----|---------|
| (a) | Per agent, with an optional `namespaces` filter per rule | Bounds the agent's total change; an admin can carve rules per tenant | Tenants compete for one rule's room |
| (b) | Per agent and namespace, always | Tenants isolated | An agent in 200 namespaces gets 200 budgets: the blast radius is unbounded again |
| (c) | Per Pipeline | Matches "per environment" read literally | Same as (b) |

**Choice: (a).** For isolation, register one AgentIdentity per tenant, each with its own subjects
and scope (17-actors §3), or write one rule per tenant with `namespaces`. Requests are served in
order of arrival; a fair share between namespaces (as `pkg/reconciler/fairqueue` does for work
queues) is a follow-up if the scale suite shows one tenant starving others.

**What one tenant learns about another.** `status.usage` is public by default (12). It carries
Bundle UIDs, environment names and times, not namespace, Pipeline or Bundle names, so a reader
sees how busy the agent is, not where. `kardinal budget` resolves names only for objects the
caller may read. With `actors.directory.public: false`, only those bound to the directory reader
see it.

### 15.2 Several installations and shards

**Shards are releases.** A sharded cluster runs one Helm release per shard (`docs/sharding.md`:
`kardinal-promoter` for the default shard, `kardinal-payments` for a second), each with its own
`--agent-index` and release name. The agent index reconciler is not shard-gated, so each shard
builds its own index (`cmd/kardinal-controller/main.go:784-792`). This section first keyed the
ledger by release and ran the budget reconciler in the default shard only: a non-default shard's
steps would then read an entry nobody writes, and wait forever (A1).

**One ledger per installation.** `status.usage` is keyed by the **ledger name**,
`--agent-budget-ledger` (chart `actors.budgets.ledger`), which defaults to the release name: in
the controller, its `--agent-index`, which the chart sets to the release's full name
(`agentbudget.Ledger`).
Every shard of one sharded installation sets it to the default shard's release name. The budget
reconciler runs only in the controller that owns cluster-scoped kinds
(`Gate.OwnsClusterScoped`, `pkg/shard/shard.go:242`) and whose release name is the ledger, so
exactly one writer grants for the installation; the other shards' step reconcilers and gates read
that entry from their caches. A sharded controller's cache is cluster-wide
(`cmd/kardinal-controller/manager_options.go:158-194`: `--namespace-shard` narrows only the
Lease cache, and only `--watch-namespace` narrows the rest), so the writer sees every shard's
steps. A shard whose ledger names a release that does not run the reconciler holds its agent
steps with `waiting for agent fixer's budget to be evaluated` (fail closed), and `kardinal doctor`
reports every AgentIdentity with budgets whose entry for the ledger is missing or behind its spec
generation. Every shard of one installation sets `actors.budgets.enabled` and
`actors.budgets.ledger` alike, as it already sets `admission.controllerUsernames` alike
(`docs/sharding.md`): a shard with budgets off holds none of its agents' steps.

**Separate installations** (a release per team with `--watch-namespace`, or two unrelated
installations) keep their own ledgers, because each sees only its namespaces and cannot count the
others'. An agent that works through two of them has twice the budget; the docs say to set
budgets with that in mind. Options considered: one global ledger (impossible without
cluster-wide visibility for every installation), one installation designated as the counter (an
installation that cannot see the namespaces it counts), per installation (chosen), with shards of
one installation sharing one.

**At most 16 ledgers.** `status.usage` holds at most 16 entries (W1, `MaxProperties`). With the
3,000-grant bound across them (14), this keeps one AgentIdentity under etcd's 1.5 MiB. Sixteen
is far more installations than one agent should span, since each counts the agent separately and
gives it a budget of its own. A 17th installation's status write is refused by the API server,
so it cannot record a grant and its steps for that agent hold (fail closed, as a missing ledger
does), with `waiting for agent fixer's budget to be evaluated`. Its reconciler reports the refused
write as a Warning event on the AgentIdentity (it cannot write a condition there), and
`kardinal doctor` reports the missing ledger (15.2). The fix is to remove a stale installation's
ledger, or to use one ledger for installations that are shards of one.

## 16. What this does not protect against

- **An unregistered agent.** It is a human or a bot (17-actors §11) and has no budget.
- **Several releases.** Each grants its own budget (15.2).
- **Changes outside kardinal.** A budget limits promotions kardinal makes. An agent with direct
  write access to the GitOps repository or the cluster is not limited by it.
- **Large single changes.** A budget counts versions and targets, not the size of a diff. The
  change risk score (v0.14) is the measure for that.
- **Index and cache lag.** A grant written a moment ago is seen by another shard's step
  reconciler when its cache catches up; the step waits that long, never longer than its
  one-minute fallback.

## 17. Upgrade

### 17.1 From v0.13

- Every new field is optional: `spec.budgets`, `spec.budgetExemptions`, `status.usage`,
  `PromotionStep.status.budget`. An AgentIdentity without budgets holds nothing (7.2 step 2).
- The CRDs change and are applied before the chart, as every upgrade requires.
- Graphs built before the upgrade carry no folded fleet limits; the grant still applies to their
  steps, because the hold is in the step reconciler, not the Graph (the reason freezes are
  enforced there too, 18-actor-policy §9.2).

### 17.2 Seeding the ledger

When an entry has no ledger yet (an upgrade, or a budget just added), the reconciler records a
grant with `seeded: true` for each step of the agent's Bundles that already started in a rule's
environments (state past Pending, started inside the window, or not settled). A PromotionStep
has no `status.startedAt` (as built), so a seeded grant's `grantedAt` is the earliest
`status.steps[].startedAt`, else `status.stateTransitionTime`, else the step's creation. Seeding
runs when the ledger is new and after every spec change (a rule just added counts what it
matches), and skips a Bundle and environment the ledger already holds. So a budget added at noon
counts the morning's promotions that still have steps; ones already retired are not counted. The
docs say so.

### 17.3 Later

- When kro's Graph CEL gets a clock (KREP-025, ledger G8), windows could be evaluated in the
  Graph, but the claim would still need the ledger (G21). No change planned.
- When kro fixes G18, G19 and G20 with a field-filtered wake, section 9 (a) can be reconsidered
  for display in the Graph.

### 17.4 Defaults: off (added by the audit)

Budgets are off at two levels, and nothing changes for anyone until both are on:

1. **The release.** Chart value `actors.budgets.enabled` (default `false`), flag
   `--agent-budgets`. Off, the budget reconciler does not run, `holdForBudget` passes (7.2 step
   0), the translator folds nothing (8), `budget.applies` is false (10), and no new watch or index
   is registered. The chart then also renders `<release>-agent-budgets-off`, a policy bound with
   `validationActions: [Warn]`, which answers a write that sets `spec.budgets` or
   `spec.budgetExemptions` with a warning ("agent budgets are off in this release
   (actors.budgets.enabled): they are kept but not enforced"), not a refusal: AgentIdentity is
   cluster-scoped and shared by every installation, and one installation with budgets off must not
   refuse an edit another installation enforces. The exemption checks of `<release>-agent-budgets`
   (12) apply either way.
2. **The agent.** An AgentIdentity without `spec.budgets` is never held, with budgets on or off
   (7.2 step 2).

Turning budgets off with budgets set stops enforcement at once (the hold passes) and keeps the
ledger as it was; `kardinal doctor` lists the AgentIdentities whose budgets are not enforced.
Turning them on seeds the ledger (17.2) before any step is held. The upgrade from v0.13 is
therefore inert: new CRD fields, no new behaviour.

## 18. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| C1 | Counting source | live objects; Bundle evidence; ledger in status; AuditEvents | **Ledger** (5): deletion refunds nothing, and the counter is the admitter |
| C2 | Window | tumbling UTC; rolling; token bucket | **Rolling** (6.1): no boundary burst; the ledger has the times anyway |
| C3 | Window roll-over without a clock in a gate | gate compares `now`; reconciler writes boundaries and requeues; Graph CEL time (KREP-025) | **Reconciler writes `evaluatedAt`, `windowStart`, `freesAt`, `nextEvaluationAt` and requeues** (6.2); gates and steps compare stored fields only. KREP-025 is not in the pin and would not claim |
| C4 | Enforcement and the race | gate CEL; admission; Graph wave over a ref; step hold plus single-writer grant | **Step hold plus grant** (7): the only option with no double admission; conditional writes make it safe across handovers |
| C5 | Graph access | scalar ref on AgentIdentity; namespaced mirror; fold at translation, no ref | **Fold, no ref** (9): a ref cannot enforce, needs G18/G19 RBAC, and wakes every Graph on every write (G20) |
| C6 | Fleet combination | grant only; fold only; both | **Both** (8): the fold creates fewer waiting steps and works without the reconciler; the grant enforces agent-wide counts and rates |
| C7 | Exhaustion | fail the step; hold the step; hold the Bundle | **Hold the step** (11.1): a rate is not a failure; environments without a rule proceed |
| C8 | Human override | gate override; exemptions on the AgentIdentity; edit the budget | **Exemptions** (11.3): only those allowed to lift the limit can, each one recorded; same shape as freezes |
| C9 | `maxUnavailable` meaning | per fleet per version (fold only); agent-wide; both | **Both**: agent-wide at the grant, folded into each fleet (8) |
| C10 | Aggregation across tenants | per agent with a namespace filter; per agent and namespace; per Pipeline | **Per agent** (15.1): anything finer makes the blast radius unbounded |
| C11 | Several releases | one global ledger; a designated release; per release | **Per release** (15.2): a release can count only what it sees |
| C12 | Names in the public ledger | namespaces and names; UIDs only; a separate private object | **UIDs only** (15.1): one object, as the plan asks, without naming other tenants' work |
| C13 | Agent rollbacks | exempt; budgeted | **Budgeted** (11.3): `rollbackOf` is asserted by the client |
| C14 | Grant order | arrival order, strict per rule; priority by environment; fair share per namespace | **Arrival order, strict per rule** (7.3): simple and starvation-free within a rule; fair share is a follow-up |
| C15 | The ledger in a sharded cluster (audit A1) | per release (each shard its own); the default shard's release, named by every shard; a ledger object of its own | **The default shard's release, named by `--agent-budget-ledger`** (15.2): one writer for the installation, and every shard's steps reach it |
| C16 | Share of a fleet or of prod (audit A11) | `fleetShare` per fleet; a share of every matching environment; absolute counts | **`fleetShare` for fleets, absolute counts for prod** (8.1): a fleet's size is fixed in its spec; all of prod is not |
| C17 | Default (audit A10) | on when `spec.budgets` is set; a release switch, off; a feature gate | **A release switch, off, plus per-agent opt-in** (17.4); a warning, not a refusal, for budgets set while off |

## 19. Test plan

The suites, as they exist on main at the audit (2026-10-21), and what each adds:

| Suite | Where | Adds |
|-------|-------|------|
| Unit | `pkg/reconciler/agentbudget` (new), `pkg/reconciler/promotionstep`, `pkg/graph`, `pkg/reconciler/policygate`, `pkg/reconciler/agentidentity`, `cmd/kardinal/cmd` | the lists below |
| Chart | `test/helm` (rendered policies; `-tags envtest` on 1.35 and 1.37 for the admission rules, as `actor_envtest_test.go` does) | the exemption policy and the off warning |
| Fake-client journeys | `test/e2e/journeys_test.go` | journeys 1-8 below |
| Scenario | `test/scenario/enterprise/agentcases`, run by `agents/load` (SCEN-AGENT-LOAD-01), `agents/scale` (SCEN-AGENT-SCALE-01) and the in-process day (`daycases`) | #2116's rows, the index-reconcile count, the day's budget episode |
| Live | `core` (`test/e2e/live/agent_budgets_test.go`, new, next to `actors_test.go`), `shard`, `upgrade-agents` (#2372), `scale` | BUDGET-01..14 |
| Soak | `soak` (#2403, `TestSoak_EnterpriseDay`, planned) | an episode and four invariants |

**Unit** (table-driven, testify, `-race`):

- Rule matching: environments, fleet environment vs target name, namespaces, multiple rules.
- Window counting over a fake clock: rolling counts, `windowStart`, `freesAt`,
  `nextEvaluationAt` with and without waiting requests, grant pruning, a clock step backwards.
- Granting: strict order per rule, a blocked rule not blocking others, version grant once per
  (Bundle, environment), fleet-target grants, `maxUnavailable` set and cleared through the
  Pipeline's `status.environments`, exemptions (match, expiry cap, removal), `LedgerFull`.
- Idempotency: the same inputs write nothing on a second reconcile.
- Conflict: a stale `resourceVersion` makes the write fail and the recomputation grant no more
  than the limit (the property test below breaks it on purpose).
- Seeding: started steps inside and outside the window, settled and not.
- `holdForBudget`: non-agent, no budgets, missing identity, stale generation, no matching rule,
  granted, waiting; message stability; recreated step finds its grant.
- Translator fold: `minPositive` with and without fleet limits, percentage `maxUnavailable`,
  several matching rules, human Bundles unchanged.
- `budget.*` context: every key present for people's versions, `applies`, `exhausted`,
  `budget.rules`, read only when referenced; with budgets off, `applies` false for an agent with
  budgets.
- `fleetShare`: resolution against the fleet's targets (rounded up, at least one), the fold into
  `maxConcurrent`, the grant check per version and fleet, a fleet grown after translation.
- The ledger name: a controller whose release is not the ledger, or that does not own
  cluster-scoped kinds, never grants; a shard reads the ledger's entry, not its own release's.
- The total cap: three ledgers on one AgentIdentity, the one that would pass 3,000 grants is
  `LedgerFull`, the others keep granting.
- Off (17.4): no reconciler, no watch, no index registered; the hold passes.
- The agent index reconciler (W0): a write that changes only `status.usage` enqueues no index
  reconcile; a spec change, a create and a delete do.
- Admission policy (rendered in `test/helm`): admin edit, exemption by an exempter, forged
  `createdBy`, exemption by the sponsor and by a subject (denied), change of an existing
  exemption (denied), a non-exemption edit by an exempter (denied), controller exemption.

**Property test (must be able to fail).** N goroutines play the step side for one agent with
`maxConcurrent: 1` against a fake client that enforces `resourceVersion`, and the reconciler runs
with two "leaders" for part of the run. Assert at most one unsettled grant at any time. Break it
on purpose (drop the optimistic lock) and show the test fails, then passes once reverted
(ENGINEER-BRIEF rule).

**Fake-client journeys** (`test/e2e/journeys_test.go`):

1. An agent ships its fifth version of the day to prod; the sixth waits with the message, and
   ships once the oldest grant is 24 hours old (fake clock).
2. Two agent versions in two namespaces reach prod at once with `maxConcurrent: 1`: one starts,
   the other waits until the first is Verified.
3. A fleet of 20 targets with `fleetTargetsPerHour: 5`: five start, the rest wait, the next five
   start an hour later.
4. Agent-wide `maxUnavailable: 1`: a failure in one Pipeline's fleet stops the agent's targets in
   another Pipeline; a person's version there is not held; a newer version in the failed
   environment releases it.
5. An exemption lets one version through, writes `BudgetExempted`, and expires.
6. A person's version is never held; a superseded waiting version leaves the queue.
7. `fleetShare: "20%"` on a fleet of 10 targets: two of the agent's targets start, the third
   waits for one to verify; a person's version in the same fleet is paced by the fleet alone.
8. Budgets off: an agent with a `versionsPerDay: 1` budget ships twice; turning budgets on seeds
   the ledger and holds the third.

**Scenario** (`test/scenario/enterprise/agentcases`; #2116 waits on these, its rows move to
`covered` with them):

| id | Run by | feature |
|----|--------|---------|
| SCEN-AGENT-BUDGET-01 | `agents/load` (CI's scenario-enterprise job at full size) | In a burst, an agent with `maxConcurrent: 1` and `versionsPerDay: 3` on prod: its prod steps beyond the limits are **held** (Pending, the budget message), never refused or failed, while the other agents' versions converge within the burst's round budget, unaffected; at every round its unsettled grants are at most 1 and its grants in the window at most 3 |
| SCEN-AGENT-BUDGET-02 | `agents/load` | The held versions proceed when the window rolls over (the scenario's fake clock) and when a granted one settles; a superseded held version leaves the queue without a grant |
| SCEN-AGENT-BUDGET-03 | `agents/scale` (full, in the release gate: 5,000 AgentIdentities until #2372's compact index entries merge, then 7,500) | 50 agents with budgets through two bursts: at most 3 usage writes per promotion on average, ledgers within their bounds, and **no agent index reconcile caused by a usage write** (the scenario's per-kind reconcile counts) |
| SCEN-DAY-BUDGET-01 | `daycases` (the in-process day) | The day's shipper agent exhausts its prod budget mid-day; people and other agents ship meanwhile; an exemption lets one version through and records `BudgetExempted` |

As built (W9): the scenario World runs budgets with `Options.Budgets` (the budget reconciler on the
release's ledger, the step hold, `budget.*`, the fold, and the chart rendered with
`actors.budgets.enabled`), and models each budget watch from what a write changed: the budget
reconciler hears its agents' steps (`stepBudgetChanged`) and, while it holds an unavailable grant, the
Pipelines; a step hears the write that grants it and, while it waits, its ledger becoming current; a
gate that reads `budget.*` hears its agent's ledger; and the agent index rebuilds only when
`agentidentity.IdentityChanged` (W0) passes. SCEN-AGENT-BUDGET-01 and -02 are one run
(`agentcases.RunBudgets`), with the burster on Pipelines of its own. SCEN-AGENT-BUDGET-03
(`agentcases.RunBudgetScale`) runs 5,000 AgentIdentities in full, under the 1 MiB index bound that
today's object format fills at about 5,200; the 7,500-identity size depends on #2372's compact entries,
and runs once W9 is rebased onto it (the bound is never raised in process: a size no real cluster can
have proves less than it seems). It measured 2.0 usage writes per promotion. SCEN-DAY-BUDGET-01's shipper is an agent of its own in the day's registry, so its
budget holds no other episode's versions when the day's episodes interleave.

**Live e2e** (rows to add to `test/e2e/coverage.tsv` with the implementation; suite `core`,
COUNT=2, unless the row names another):

| id | feature |
|----|---------|
| BUDGET-01 | versionsPerDay holds the agent's next prod version in Pending with the budget message and does not fail it |
| BUDGET-02 | maxConcurrent across two namespaces: exactly one of two simultaneous agent versions starts |
| BUDGET-03 | fleetTargetsPerHour holds fleet targets beyond the hourly limit; the fold keeps steps at most maxConcurrent |
| BUDGET-04 | agent-wide maxUnavailable stops the agent's targets in a second Pipeline; a newer version releases it |
| BUDGET-05 | a budget added while versions are in flight seeds the ledger and holds only steps that have not started |
| BUDGET-06 | `kardinal override --budget` adds an exemption verified by admission; the exempt step writes BudgetExempted |
| BUDGET-07 | admission refuses exemptions by the sponsor, by an agent subject, with a forged createdBy, and non-exemption edits by an exempter |
| BUDGET-08 | `budget.*` in `approval.requiredWhen` requires approval after half the day's budget |
| BUDGET-09 | deleting a granted step or a finished Bundle refunds no budget |
| BUDGET-10 | controller restart during waiting requests: no double grant, waiting steps resume |
| BUDGET-11 | suite `upgrade-agents` (#2372): an upgrade from v0.13 with agent versions in flight changes nothing (budgets off); turning budgets on with a budget set seeds the ledger and holds only steps that have not started |
| BUDGET-12 | `fleetShare` holds an agent's fleet targets beyond the share; the fold keeps the steps created at most the share |
| BUDGET-13 | suite `shard`: an agent's versions in the default shard's and the second shard's namespaces share one ledger (`--agent-budget-ledger`): `maxConcurrent: 1` lets exactly one of two start, across the shards |
| BUDGET-14 | suite `upgrade-agents`, before BUDGET-11 turns budgets on: budgets off (the chart default): an AgentIdentity with budgets is admitted with the off warning, and nothing is held (core runs with budgets on, so it is checked where they are still off; no second release in core) |

**Scale** (suite `scale`, under LOADLOCK): 2,000 AgentIdentities with budgets, 50 agents each
promoting into 20 namespaces; assert status size, writes per promotion (at most 3 on average),
grant latency (p99 under 2 s), and that no AgentIndex rebuild follows a usage-only write. The
7,500-identity case is the in-process SCEN-AGENT-BUDGET-03, once #2372 merges (5,000 before it).

**Soak** (#2403, `TestSoak_EnterpriseDay`, suite `soak`; its design is the issue's first comment).
Budgets on for the soak's release. team-a's shipper agent gets a prod rule with
`maxConcurrent: 1`, `versionsPerDay` sized to run out at about T+50, and, if an agent ships to
team-c's fleet, `fleetShare: "20%"`.

- **Episode T+50, budget exhausted:** the shipper's next prod versions hold with the budget
  message while people's and the other agents' versions ship; at T+52 an exemption (`kardinal
  override --budget`, by an exempter outside the agent's party) lets one through and expires at
  T+57.
- **Across the T+30 upgrade** (from v0.13, which has no budgets): the release starts with budgets
  off; they are turned on at T+35, which seeds the ledger.
- **Invariants, added to the 3-minute checker (`invariants.Every`):**
  1. For each agent and rule, at every pass: unsettled grants at most `maxConcurrent`, failed and
     unreplaced at most `maxUnavailable`, grants in any rolling window at most the window's limit,
     and per version and fleet at most the resolved `fleetShare`.
  2. No step is held for budget unless its Bundle is an agent's and a rule matches; no budget hold
     ends `Failed`; every held step eventually starts, is superseded, or is still within a window
     that has not rolled over at the end.
  3. Every ledger within its bounds (2,000 grants, 3,000 per AgentIdentity) and every
     AgentIdentity under 600 KB.
  4. The agent index reconciler's reconcile count does not track `status.usage` writes (its
     rate over each 10-minute window stays at the rate of AgentIdentity spec changes).

## 20. Work breakdown (issues, milestone v0.14.0)

| # | Issue | Size | Depends on |
|---|-------|------|------------|
| W0 | `fix(controller)`: the agent index reconciler's AgentIdentity watch passes creates, deletes and spec generation changes only (A2); a test that a status-only write enqueues no index reconcile | S | none (lands before W2) |
| W1 | `feat(api)`: `spec.budgets` (L1-L5, `fleetShare`), `spec.budgetExemptions`, `status.usage` keyed by ledger, `PromotionStep.status.budget`; CRD rules (unique names, limits, sum bound, `fleetShare` pattern); generated docs | M | v0.12 actors |
| W2 | `feat(controller)`: `pkg/reconciler/agentbudget`: rules, rolling windows, ledger, seeding, grants with optimistic lock, `fleetShare` at the grant, exemptions, Events, conditions under `usage`; runs only where the release is the ledger and owns cluster-scoped kinds; `--agent-budgets` and `--agent-budget-ledger`; the per-AgentIdentity cap; new indexes (PromotionSteps by `status.budget.agent`, Bundles by UID, AgentIdentities with an unavailable grant); idempotency and property tests | L | W0, W1 |
| W3 | `feat(controller)`: `holdForBudget` in `handlePending`; the step reconciler's AgentIdentity watch that wakes newly granted steps; `BudgetExempted` audit outbox | M | W1, W2 |
| W4 | `feat(graph)`: fold `maxConcurrent` (with `fleetShare`) and `maxUnavailable` into fleet DAG entries for agent Bundles | S | W1 |
| W5 | `feat(policy)`: `budget.*` in the PolicyGate context; its AgentIdentity watch with a predicate on this ledger's usage, and the Bundle index on `spec.actor.agent`; `policy simulate --as-agent` reads usage | M | W1, W2 |
| W6 | `feat(chart)`: `actors.budgets.enabled` (false) and `actors.budgets.ledger`; the `<release>-agent-budgets` policy, the `manage` and `exempt` checks, the exempter ClusterRole; the warn-only `<release>-agent-budgets-off` policy | M | W1 |
| W7 | `feat(cli)`: `kardinal budget`, `kardinal override --budget`; `why` and `status` show the hold; `doctor` reports budgets not enforced and ledgers missing or behind; UI agent page numbers | M | W2, W3 |
| W8 | `docs`: user page `docs/agent-budgets.md` (with the shard and multi-installation rules, 15.2), changelog, the ledger rows | M | W2-W7 |
| W9 | `test`: the scenario rows SCEN-AGENT-BUDGET-01..03 and SCEN-DAY-BUDGET-01 (closes #2116), BUDGET-01..14 across `core`, `shard`, `upgrade-agents` and `scale`, and the soak's budget episode and invariants (#2403) | L | W2-W7 |

## 21. Out of scope here (follow-ups)

- Fair share between namespaces for waiting requests (C14).
- Budgets that adapt to the agent's track record (earned autonomy, v0.14, in
  [25-change-risk.md](25-change-risk.md) §7).
- `prodShare`: a share of every matching environment across Pipelines (8.1 (b)).
- A create-rate limit on Bundles (needs a webhook; section 1).
- Notifications on exhaustion through NotificationHooks (an event type `BudgetExhausted`).
- A `ref` from the Graph for display once kro fixes G18, G19 and G20 (section 9).
