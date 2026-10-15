# Design: Agent budgets and blast radius

> Status: Proposed (spec for v0.14.0, AGENTIC-PLAN §5). Implementation waits until v0.13.0 is
> tagged, and follows in separate PRs (section 20).
> Created: 2026-10-15
> Depends on: [17-actors.md](17-actors.md) (`AgentIdentity`, the verified `spec.actor` stamp on
> Bundles, the one-party rule) and [18-actor-policy.md](18-actor-policy.md) (`agent.*` read by the
> PolicyGate reconciler, §2.2; the freeze hold and its exemptions, §9).
> Verified against: this repository at `8f5d86e` (`pkg/graph/compact.go`,
> `pkg/reconciler/promotionstep`, `pkg/reconciler/bundle`, `pkg/reconciler/policygate`,
> `pkg/shard`, `api/v1alpha1`), kro v0.10.0-rc.0 (`54a203b`) and
> Kubernetes `k8s.io/apiserver` v0.37.1 (the version in `go.mod`).
> Source references are `path:line` in those trees.

---

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
| **Grant** | The budget reconciler's decision that a request fits. It is an entry in the AgentIdentity's `status.usage.<release>.grants` |
| **Ledger** | The list of grants. Usage is counted from it, not from live objects |
| **Window** | The rolling period a rate limit counts over: 24 hours for L1, one hour for L3 |
| **Release** | One kardinal installation, named by its `--agent-index` (the release name, 17-actors §5.4) |

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
  states the Graph counts as failures, `compact.go:473-476`) while the Pipeline's
  `status.environments` entry for that environment still names this Bundle. A newer version in
  that environment, from anyone, or the Pipeline's deletion, ends it. Fleet targets have one
  entry each (`pkg/reconciler/pipeline/environments.go:34-45`).

A limit that is not set does not apply. A rule with no limit set is refused by the schema.

**Validation (CRD schema and CEL rules).**

- Rule names unique (`self.all(r, self.exists_one(s, s.name == r.name))`), at most 16 rules, at
  most 64 entries in each pattern list, `*` only at the end of an entry.
- At least one of the four limits per rule.
- The sum over all rules of the four limits is at most 1,500
  (`self.map(r, ...).sum() <= 1500`; `sum` is in the Kubernetes CEL lists library,
  `k8s.io/apiserver/pkg/cel/library/lists.go:145`). This bounds the ledger (section 14).
- Exemption names unique; `namespace`, `pipeline`, `environment`, `reason` (at most 256
  characters), `expiresAt` and `createdBy` required.

### 4.2 Status

```yaml
status:
  ready: true                       # 17-actors §3, unchanged
  usage:
    kardinal-promoter:              # one entry per release (section 15.2)
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
      exemptions:                   # the exemptions in effect, as this reconciler decided
        - {name: inc-4211, firstSeenAt: "2026-10-15T09:00:04Z", expiresAt: "2026-10-15T12:00:00Z"}
      conditions: [...]             # Current; LedgerFull
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
}

type AgentBudgetExemption struct {
	Name, Namespace, Pipeline, Environment string
	Bundle                                 string `json:"bundle,omitempty"`
	Reason                                 string
	ExpiresAt                              metav1.Time
	CreatedBy                              string
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
//   Usage map[string]AgentUsage `json:"usage,omitempty"` // key: release

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
  not check DELETE, `chart/kardinal-promoter/templates/identity-admission.yaml:381-384`) or a
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
| PromotionSteps with `status.budget.agent == <agent>` | requests (Waiting) and their order | cache field index on `status.budget.agent` |
| The step of a grant (`kardinal.io/bundle-uid` and `kardinal.io/environment` labels, `LabelBundleUID`, `pkg/graph/compact.go:60`) | settled or not, failed or not | cache, by labels |
| Bundles by UID | namespace and Pipeline of a grant, for the unavailability check | cache field index on `metadata.uid` |
| Pipelines | `status.environments[].bundle` for L4 recovery | cache |

A PromotionStep is created and written only by kro and the controller
(`<release>-graph-objects`, `identity-admission.yaml:370-420`), so `status.budget` cannot be
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

**Clock.** One writer per agent and release (the default shard's leader, 15.2) reads the clock,
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
(`pkg/reconciler/bundle/reconciler.go:1188-1215`, `1291-1309`), and the freeze hold reads state
another reconciler wrote (18-actor-policy §9.2). (d) generalises the first across namespaces by
putting the decision in one object's status instead of a process lock, because an agent's steps
may be reconciled by different shards (`pkg/shard/shard.go`) that share no memory.

**What kro `readyWhen` can enforce here:** a predicate over a snapshot of the node's own object
(G3), and in a standalone Graph not even a hold on dependents (G1). Neither is a reservation.

### 7.2 The step side: `holdForBudget`

In `handlePending` (`pkg/reconciler/promotionstep/reconciler.go:884`), after the admission wait
(`spec.admitted`, so a step the fleet wave may still prune never asks), the pause, hold and slot
holds (`:903-910`), image verification, pre-hooks and the required-gate re-check (`:931-960`),
and before the transition to Promoting:

1. Read the Bundle (already read by `holdForSlot`, `lifecycle.go:102`). If `spec.actor.kind` is
   not `agent`, pass.
2. Read `AgentIdentity/<actor.agent>` from the cache. If it has no `spec.budgets`, pass: no rule
   can hold it. If it is missing, hold with `agent fixer is not registered (AgentIdentity fixer
   not found)`: the `kardinal-agents` gate (18-actor-policy §7) already blocks such a version,
   and this keeps a step created before deregistration from starting.
3. Read `status.usage.<release>`. If it is missing or its `observedGeneration` is not the spec's
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

**Waking.** The step reconciler watches AgentIdentities. Its update handler compares the old and
new `grants` of this release and enqueues only the steps of new grants (found by the Bundle UID
and environment labels). A grant wakes one step; a status write that grants nothing wakes none.

### 7.3 The reconciler side: granting

A new controller, `pkg/reconciler/agentbudget`, reconciles AgentIdentities (one work-queue key
per agent, so one agent is never reconciled twice at once). It runs only in the shard that owns
cluster-scoped kinds (`shard.DefaultShard`, `pkg/shard/shard.go:79-81`), and only on the leader.
Several workers are safe: each works on a different agent. It writes only
`status.usage.<release>` and `status.conditions` entries of its own type.

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
6. If anything changed, write the release's entry with a JSON merge patch carrying the
   `resourceVersion` it read (controller-runtime `MergeFromWithOptimisticLock`). The API server
   refuses a stale write with Conflict (`k8s.io/apiserver/pkg/registry/generic/registry/store.go:807-808`);
   the reconciler re-reads (uncached) and starts over. An unchanged result writes nothing.
7. `RequeueAfter` until `nextEvaluationAt`.

The conditional write is what makes the grant safe even if two writers exist for a moment (a
leader handover, or a second release writing its own entry of the same object): a grant computed
from a stale ledger never lands. Grants are never revoked; a step that found its grant may
rely on it.

**Triggers.** AgentIdentity spec changes (generation, including exemptions); PromotionSteps whose
`status.budget` or `status.state` changes and that carry `status.budget.agent` (mapped to that
agent); Pipeline `status.environments` changes, mapped to the agents that hold an unavailable
grant (a cache index on AgentIdentities with any `unavailable: true` grant; the ledger has no
Pipeline names, C12, so the mapping enqueues those few agents and their reconcile resolves the
Bundle UIDs); its own requeue, and a resync every 5 minutes while seeded grants are unsettled
(17.2), because a seeded step has no `status.budget` and so sends no event the mapping sees.

### 7.4 What this does not hold

Steps already past Pending when a budget is added or tightened keep running; they are counted
(seeding, 17.2). A granted step that is then paused or frozen keeps its `maxConcurrent` place
until it settles: the place is taken by a change in progress.

## 8. Fleets: combining with the fleet pacing in `compact.go`

The compact shape paces a fleet per Bundle, in the Graph:

- `maxConcurrent`: at most that many of the fleet's targets in flight (a step exists and is not
  Verified or Superseded; a Failed target keeps its place), `compact.go:342-361`.
- `maxUnavailable`: once that many of the fleet's targets failed, none starts,
  `compact.go:355-361` with `failedFleets` at `:473-476`.
- `paused`: `spec.pausedFleets`, `compact.go:279-283`.

These bound one version in one fleet. The agent's limits bound all of its versions in all fleets.
They combine in two places:

1. **Fold at translation (the Graph).** For a Bundle stamped `kind: agent`, the translator reads
   the AgentIdentity (from the cache, as it reads the Pipeline) and writes into each fleet
   target's DAG entry `maxConcurrent = minPositive(fleet.maxConcurrent, rule.maxConcurrent)` and
   `maxUnavailable = minPositive(resolved fleet.maxUnavailable, rule.maxUnavailable)` over the
   rules that match the fleet (`compactStep.maxConcurrent` and `.maxUnavailable`,
   `compact.go:197-202`, `279`). The existing wave filter then creates fewer steps. An agent-wide
   bound of N implies a per-fleet bound of N, so the fold never admits what the grant would
   refuse; it only keeps steps from being created just to wait.
2. **Grant at the step.** The ledger check (7.3) enforces the agent-wide count across fleets,
   Pipelines and namespaces, and `fleetTargetsPerHour`, which the Graph cannot (it has no clock,
   G8).

The fold is a snapshot of the AgentIdentity at translation. A **tighter** budget applies at once
through the grant; a **looser** one reaches an in-flight Graph only when it is rebuilt. The
direction of staleness is the safe one, so AgentIdentity edits do not rebuild Graphs.

**Held targets and fleet places.** A target held for budget is a step that exists and is not
settled, so it keeps its fleet place, exactly as the fleet's `maxConcurrent` means: no target
behind it starts. Waiting steps per fleet are therefore at most the folded `maxConcurrent`. A
fleet with `maxConcurrent: 0` (every target at once) and only `fleetTargetsPerHour` set creates
every target's step and holds most of them; the docs recommend setting `maxConcurrent` with
`fleetTargetsPerHour`.

**Pacing presets.** The AWS-style wave presets planned for v0.14 (1, 1, 3, 12, rest) produce
fleet entries the same way; the fold applies to whatever they produce.

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
(`pkg/reconciler/policygate/reconciler.go:496`) and declares it in `newEvaluator`
(`cel_evaluator.go:108-114`) as `DynType`, read only when the expression or
`approval.requiredWhen` contains `budget` (as `changewindow` is, `reconciler.go:602`):

| Variable | Type | Value |
|----------|------|-------|
| `budget.applies` | bool | the version is an agent's, and a rule of its AgentIdentity matches the gate's environment (`kardinal.io/environment` label of the instance) |
| `budget.agent` | string | `actor.agent`; `""` |
| `budget.current` | bool | `status.usage.<release>.observedGeneration` equals the spec generation |
| `budget.env.rule` | string | the first matching rule in spec order; `""` |
| `budget.env.versions`, `.concurrent`, `.fleetTargets`, `.unavailable` | map | `{set, limit, used, remaining}`; `set: false` and zeros when the limit is not set |
| `budget.env.waiting` | int | requests waiting under the rule |
| `budget.env.exhausted` | bool | any set limit of the rule has `remaining == 0` |
| `budget.rules.<name>` | map | the same shape, for every rule of the agent |
| `budget.evaluatedAt` | string | RFC 3339, for messages only |

Every key except under `budget.rules` is always present, so expressions evaluate (to false) for
people's versions instead of failing with "no such key". `used` is the count before this version:
a gate is evaluated before the step exists and asks for room.

The values come from status only (`status.usage`), the same source the step uses. The
reconciler's AgentIdentity watch (18-actor-policy §2.2) gains a predicate on `status.usage` and
maps an event to the instances whose expression or `requiredWhen` contains `budget` and whose
Bundle is the agent's (`instanceGateRequests`, `reconciler.go:1337`, `1390`).

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
  `min(expiresAt, firstSeenAt + 24h)` (admission has no clock, so the cap is applied here), and
  writes the ones in effect to `status.usage.<release>.exemptions`.
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

The chart adds a ClusterRole `<release>-agent-budget-exempter` (`get, patch` and the custom verb
`exempt` on `agentidentities`) for incident commanders. Because `patch` alone lets them write the
whole object, the policy's `manage` check is what keeps them to exemptions.

**Who writes `status.usage`.** Only the controller has `update` on `agentidentities/status`
(17-actors §5.4, Writers). **Who writes `status.budget`** on steps: the controller, under the
existing `<release>-graph-objects` policy.

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
| The ledger reaches 2,000 entries | `LedgerFull` condition; new requests wait; existing grants keep working | Fail closed; the schema bound (4.1) makes this a misconfiguration signal |
| The AgentIdentity is deleted | Its steps that have not started hold; started ones run | 17-actors §3 requires suspension first; the `kardinal-agents` gate also holds such versions |
| The agent is suspended | Admission refuses its writes (17-actors); gates see `agent.suspended`; the ledger is kept | Suspension is the kill switch; budgets are a rate |
| A request's Bundle is superseded | The step settles as Superseded; it leaves the queue | No grant wasted |

## 14. Scale and the cost of a status write

**Writes per promotion.** One status write grants a batch of requests (the work queue coalesces
events per agent); one marks grants settled; a failure and its recovery add one each; window
drops add at most one every 5 minutes while nothing waits. About two writes per agent promotion.
At 1,000 agent promotions an hour across a cluster: about 2,000 writes an hour, 0.6 a second.
The step adds one status write when it starts waiting (the message does not change while counts
move, 7.2).

**Object size.** A grant is about 150 bytes of JSON. The ledger holds at most the grants some
window or state needs, which the schema bounds by the sum of all limits (1,500, section 4.1),
plus exempt grants, under a hard cap of 2,000 entries: about 300 KB, a fifth of etcd's
1.5 MiB object limit. Typical agents hold tens of entries.

**Who receives each write.** Every kardinal controller replica's cache (AgentIdentities are
already cached for the AgentIndex, 17-actors §5.4); the step reconciler's handler (it wakes only
newly granted steps); the PolicyGate mapper (only instances that read `budget` for that agent).
The AgentIndex builder must ignore writes that change only `status.usage` (a predicate on spec
generation and `status.ready`), or every promotion would rebuild the index. kro receives nothing:
no Graph reads the AgentIdentity (section 9).

**Many agents.** The reconciler's work per agent is proportional to its requests and grants,
from indexed cache reads. 2,000 AgentIdentities (the actors scale target) with budgets cost
nothing while idle: a requeue only when a count must drop.

**Many targets.** A fleet of 500 targets with the folded `maxConcurrent` of 10 creates at most 10
held steps at a time (section 8). The step wake-up is per grant, not per write.

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

### 15.2 Several releases and shards

Each release (17-actors §5.4: one AgentIndex per release) enforces its own budget in its own
`status.usage.<release>` entry, because a release installed with `--watch-namespace` sees only
its namespaces and cannot count the others'. An agent that works through two releases therefore
has twice the budget; the docs say to set budgets with that in mind. Options considered: one
global ledger (impossible without cluster-wide visibility for every release), one release
designated as the counter (a chart value every release must agree on, and a release that cannot
see the namespaces it counts), per release (chosen).

Within one release, namespace shards (`--namespace-shard`) share the entry: only the default
shard runs the budget reconciler, and a sharded controller's cache is cluster-wide
(`cmd/kardinal-controller/manager_options.go:163-173`; only `--watch-namespace` narrows it), so
it sees every shard's steps. Other shards' step reconcilers read the entry from their caches.

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
environments (state past Pending, `status.startedAt` inside the window, or not settled). So a
budget added at noon counts the morning's promotions that still have steps; ones already retired
are not counted. The docs say so.

### 17.3 Later

- When kro's Graph CEL gets a clock (KREP-025, ledger G8), windows could be evaluated in the
  Graph, but the claim would still need the ledger (G21). No change planned.
- When kro fixes G18, G19 and G20 with a field-filtered wake, section 9 (a) can be reconsidered
  for display in the Graph.

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

## 19. Test plan

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
  `budget.rules`, read only when referenced.
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

**Live e2e** (rows to add to `test/e2e/coverage.tsv` with the implementation, suite `core`,
COUNT=2):

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
| BUDGET-11 | upgrade from v0.13 with agent versions in flight: nothing held without budgets; budgets added after the upgrade apply |

**Scale** (suite `scale`, under LOADLOCK): 2,000 AgentIdentities with budgets, 50 agents each
promoting into 20 namespaces; assert status size, writes per promotion (at most 3 on average),
grant latency (p99 under 2 s), and that no AgentIndex rebuild follows a usage-only write.

## 20. Work breakdown (issues, milestone v0.14.0)

| # | Issue | Size | Depends on |
|---|-------|------|------------|
| W1 | `feat(api)`: `spec.budgets`, `spec.budgetExemptions`, `status.usage`, `PromotionStep.status.budget`; CRD rules (unique names, limits, sum bound); generated docs | M | v0.12 actors |
| W2 | `feat(controller)`: `pkg/reconciler/agentbudget`: rules, rolling windows, ledger, seeding, grants with optimistic lock, exemptions, Events, conditions; default shard only; idempotency and property tests | L | W1 |
| W3 | `feat(controller)`: `holdForBudget` in `handlePending`; the AgentIdentity watch that wakes newly granted steps; `BudgetExempted` audit outbox | M | W1, W2 |
| W4 | `feat(graph)`: fold `maxConcurrent` and `maxUnavailable` into fleet DAG entries for agent Bundles | S | W1 |
| W5 | `feat(policy)`: `budget.*` in the PolicyGate context and its watch; `policy simulate --as-agent` reads usage | S | W1, W2 |
| W6 | `feat(chart)`: `<release>-agent-budgets` policy, `manage` and `exempt` verbs, the exempter ClusterRole; AgentIndex predicate ignores usage-only writes | M | W1 |
| W7 | `feat(cli)`: `kardinal budget`, `kardinal override --budget`; `why` and `status` show the hold; UI agent page numbers | M | W2, W3 |
| W8 | `docs`: user page `docs/agent-budgets.md`, changelog, the ledger rows; `test(e2e)`: BUDGET-01..11 and the scale case | M | W2-W7 |

## 21. Out of scope here (follow-ups)

- Fair share between namespaces for waiting requests (C14).
- Budgets that adapt to the agent's track record (earned autonomy, v0.14).
- A create-rate limit on Bundles (needs a webhook; section 1).
- Notifications on exhaustion through NotificationHooks (an event type `BudgetExhausted`).
- A `ref` from the Graph for display once kro fixes G18, G19 and G20 (section 9).
