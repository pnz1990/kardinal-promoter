# Design: Actor-aware policy, agent approvals and freezes

> Status: Implemented in v0.12.0 (epic #1843, #1869).
> Created: 2026-10-14
> Depends on: [17-actors.md](17-actors.md) (epic #1834): the `Actor` type, the `kardinal.io/actor`
> stamp on Bundles, Approvals and overrides, `AgentIdentity`, and `pkg/actor`
> (`SameParty`, `PartyKey`, `Matches`, `OrUnknown`).
> Amends: 17-actors.md §10.2 (where `agent.*` comes from) for the reason in §2.2.
> Verified against: this repository at `d21c640` and kro v0.10.0-rc.0 (`54a203b`).

---

## 1. Problem and scope

The actors spec gives every change a verified actor: kind (human, agent, bot, controller),
principal, and for an agent its name and sponsor. Nothing uses it yet. This spec makes policy
use it:

| # | Feature | Section |
|---|---------|---------|
| P1 | CEL variables `actor.*` and `agent.*` in PolicyGate expressions | 2 |
| P2 | `approval.requiredWhen`: an approval policy that applies only when a CEL condition holds | 3 |
| P3 | The one-party rule in `excludeAuthor`, and one vote per party | 4 |
| P4 | Opt-in agent approvals: `approval.allowAgents` and `approval.allowedAgents` | 5 |
| P5 | Agent overrides: break-glass is for people | 6 |
| P6 | The per-environment shorthand `agents: auto \| approve \| deny` | 7 |
| P7 | Requiring a stamp | 8 |
| P8 | Agent freeze and cluster stop-the-line: a freezing ChangeWindow, `kardinal freeze`, the UI control | 9 |

Out of scope: Verdicts and the verify gate (E8, #1844), MCP tools (E10, #1845). Both consume
P1, P3 and P4 unchanged.

Every question below compares at least three options and records the choice. Section 13 is the
decision table.

## 2. CEL variables

### 2.1 The variables

The PolicyGate reconciler adds two variables to the CEL context it builds in `buildContext`
(`pkg/reconciler/policygate/reconciler.go`), next to `bundle.*`, and declares both in
`newEvaluator` (`cel_evaluator.go`) as `DynType`:

| Variable | Type | Value |
|----------|------|-------|
| `actor.kind` | string | the Bundle's `spec.actor.kind`; `unknown` without a stamp |
| `actor.principal` | string | `spec.actor.principal`; without a stamp `kardinal.io/created-by`; else `""` |
| `actor.agent`, `actor.sponsor` | string | the stamp; `""` unless the kind is `agent` |
| `actor.via` | string | the stamp; `""` |
| `actor.verified` | bool | true when the Bundle has a stamp |
| `agent.registered` | bool | the Bundle's agent has an AgentIdentity whose `status.ready` is true for its current generation, and an entry in this release's AgentIndex |
| `agent.name` | string | `actor.agent` |
| `agent.sponsor` | string | the agent's AgentIndex entry now (`sponsor`); `""` when not registered |
| `agent.autonomy` | string | the entry's `autonomy` now: `auto`, `approve` or `deny`; `""` when not registered |
| `agent.suspended` | bool | the entry is `blocked` while the AgentIdentity is ready, which is what a suspension is |

`actor.*` is the Bundle's stamp: fixed when the version was created (the CRD forbids changing
`spec.actor`). `agent.*` is the registry as it is at evaluation, read only from status that other
reconcilers write (the AgentIdentity reconciler's `status.ready`, the AgentIndex reconciler's
entries), never computed from a spec: an admin who suspends an
agent or tightens its autonomy changes the gates of its in-flight versions. For a Bundle whose
actor is not an agent, `agent` is `{registered: false, name: "", sponsor: "", autonomy: "",
suspended: false}`. Every key is always present, so `agent.autonomy == 'auto'` evaluates (false)
instead of failing with "no such key". The asserted fields (`model`, `session`, `client`) are not in the context: they are text the client
writes, and the untrusted-input guarantee (no attacker-written text reaches a gate) keeps them out.
`bundle.createdBy` stays, an alias of `actor.principal`
for one release.

`approvals.*` gains `approvals.met` (bool: the approval policy is met, section 3) next to
`count`, `required`, `users` and `rejected`.

`kardinal policy simulate` and `kardinal explain` run the same reconciler on an in-memory
client (`cmd/kardinal/cmd/policy_eval.go`), so they see the same variables. `simulate` gains
`--as-agent <name>` (the simulated version is stamped as that agent, `agent.*` read from its
AgentIdentity) and `--as-human <user>`, so a person or an MCP pre-flight can ask "would an agent
version be allowed into prod now".

### 2.2 Where `agent.*` comes from

17-actors.md §10.2 chose a kro `ref` node `agentIdentity` in each agent Bundle's Graph, with gate
templates copying the fields into a new `PolicyGate.spec.agent`. Before building on it, the cost:

- The Graph identity reads the ref. A namespaced Graph ServiceAccount needs a **cluster-wide**
  grant to read a cluster-scoped kind (ledger [G19](16-graph-capability-ledger.md#g19-a-namespaced-graph-identity-needs-a-cluster-wide-grant-to-read-a-cluster-scoped-ref)), and kro re-reads a `ref` on change only if
  that identity may **watch the whole kind**, because the watch check drops the ref's name
  ([G18](16-graph-capability-ledger.md#g18-the-ref-watch-check-ignores-the-refs-name)). The spec's workaround is a ClusterRoleBinding whose subjects the IdentityProvisioner
  edits for every namespace with an agent Bundle.
- The value reaches the gate only after kro re-renders the gate collection. kro has one queue
  for every Graph (G13), so a suspension waits behind the promotion backlog.
- `spec.agent` is a new PolicyGate field every gate template and collection must render.

Options:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `ref` node, gate templates copy into `spec.agent` (17-actors §10.2) | The data is on the Graph and on the gate object | G18/G19 RBAC widening and a binding the controller edits; latency behind kro's queue (G13); a PolicyGate schema field; every collection node renders the spec field by field |
| (b) | The translator snapshots the AgentIdentity into the gate instances; the Bundle reconciler re-translates an agent's in-flight Bundles when its AgentIdentity changes (the #626 in-place update) | No Graph RBAC; the gate object shows the values | Latency of a re-translation plus kro's queue; a hash per Bundle to detect the change; a Graph update for every in-flight Bundle of the agent on every edit |
| (c) | The PolicyGate reconciler reads the AgentIdentity named by the Bundle's stamp from its cache, and a watch on AgentIdentities re-evaluates the gates that read `agent.` | No Graph RBAC, no schema field, no Graph update; a suspension reaches the gate in one gate reconcile | One more cross-CRD read in the gate context, outside the Graph |

**Choice: (c).** It is the same shape as the gate context's existing reads of ChangeWindows,
MetricChecks and PRStatuses ([ledger G8](16-graph-capability-ledger.md#g8-logic-still-outside-the-graph), row "Cross-CRD reads for the gate context"), it writes
only the gate's own status, and it is the fastest path for a kill switch. (a) is the
Graph-native shape and becomes right once kro fixes G18 and G19: the ledger row records the ask.
The mechanism, exactly:

- `buildContext` reads `bundle.spec.actor`. When its kind is `agent`, it gets
  `AgentIdentity/<actor.agent>` and this release's `AgentIndex` (`--agent-index`, both
  cluster-scoped) from the manager's cache, but only when the expression or
  `approval.requiredWhen` contains `agent` (as `changewindow` is read only when referenced). A
  read error other than NotFound fails the gate closed with `context error`; NotFound, or no
  `--agent-index`, gives `registered: false`.
- The values come from status only: `registered` from the AgentIdentity's `status.ready` (at
  its current generation), and `sponsor`, `autonomy` and `suspended` (an entry `blocked` while
  the identity is ready) from the agent's AgentIndex entry. A suspension is therefore whatever
  the registry's reconcilers last wrote, the same answer admission gives (17-actors §5.3).
- `SetupWithManager` watches the AgentIndex, which is rewritten on every AgentIdentity change,
  and maps an event to the gate instances whose expression or `requiredWhen` contains `agent`
  (`instanceGateRequests`, the helper the ChangeWindow watch uses).
- The controller already lists and watches AgentIdentities and AgentIndexes for the index
  (17-actors §5.4), so this adds no RBAC and no informer.

## 3. `approval.requiredWhen`

An agent version in an environment whose ceiling is `auto` needs an approval exactly when its
AgentIdentity's autonomy is `approve`, which is live (section 2). A gate's `spec.approval` today
holds the gate whenever it is set, so it cannot express "approval when X".

Options:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `approval.requiredWhen`: a CEL expression in the gate's context; the policy holds the gate only while it is true | General ("approval unless hotfix", "approval for agents only"); plain CEL; one field | The CLI and UI, which count approvals without CEL, must learn whether it held at the last evaluation |
| (b) | A native field on the gate, `spec.agentCeiling`, from which the reconciler computes the effective autonomy and applies the approval policy | Exactly the shorthand's need | A special gate kind only the translator can use well; nothing for "approval for agents only" in a team's own gate |
| (c) | The translator compiles the agent's autonomy at translation time into a static policy | No new field | Not live: tightening an agent's autonomy would not require approval for its in-flight versions |

**Choice: (a).** It is CEL only, and it is what a team writes for its own approvers:

```yaml
approval:
  required: 1
  allowedGroups: [release-managers]
  excludeAuthor: true
  requiredWhen: "actor.kind == 'agent'"   # people ship on their own; agents need a release manager
```

Semantics:

- Evaluated in the same context, after the expression. Unset means always (v0.11 behaviour).
- An evaluation error fails the gate closed, like the expression.
- While it is false the approvals are still tallied and recorded in `status.approvals`, the
  hold is not applied, and `approvals.met` is true. The reason says
  `approval not required (approval.requiredWhen is false)`.
- The reconciler writes `status.approvalRequired` (bool, set on every evaluation of a gate
  with a policy). `lifecycle.ApprovalStandingOf`, used by `kardinal inbox`, `kardinal why`,
  `kardinal status` and the UI, treats a gate whose last evaluation said `false` as an ordinary
  gate, not as an approval waiting on someone.
- The Graph copies `requiredWhen` with the rest of the policy (`approvalPolicyData`). The
  PolicyGate schema gains the string (max 4096, like `expression`).

## 4. The one-party rule

From 17-actors §10.3, with `party(a) = {principal}` for a non-agent and
`{principal, sponsor, "agent:"+agent}` for an agent:

- **excludeAuthor.** An approval does not count when `SameParty(author, approver)`. The author
  is `actor.OrUnknown(bundle.spec.actor, kardinal.io/created-by)`. The approver is
  `actor.OrUnknown(approval.actor, approval.user)` (`GateApproval.actor` is copied from the
  Approval by the Graph, 17-actors §4.1). So alice cannot approve her agent's version, her agent
  cannot approve hers, and two agents of one sponsor cannot approve each other's.
- **One vote per party.** `required: N` counts distinct `PartyKey`s: the sponsor for an agent,
  the principal otherwise. Alice and her agent are one approval; two agents of one sponsor are
  one approval. `approvals.count` and `approvals.users` count and list parties (one name per
  party: the first approver by name).
- **A reject** follows the same eligibility as an approve: a reject that would not count as an
  approval does not block.
- **Held cases stay.** With `excludeAuthor`, a version with no principal (no stamp, no
  `created-by`) or a controller/component author holds the gate, as in v0.11.
- **Reasons.** A decision that does not count says why in `status.approvals[].reason`:
  `the version's author (approval.excludeAuthor)`, `the author's sponsor (approval.excludeAuthor)`,
  `an agent of the author's sponsor (approval.excludeAuthor)`, `the author's own agent
  (approval.excludeAuthor)`, `already counted for alice@example.com (one vote per party)`.

`lifecycle.TallyApprovals(gate, author v1alpha1.Actor)` replaces the `creator string`
parameter; every caller (the reconciler, `inbox`, `why`, `status`, the UI API) passes
`actor.OrUnknown(bundle.Spec.Actor, createdBy)`.

## 5. Opt-in agent approvals

An approval whose actor kind is `agent` counts only when the gate opts in:

```yaml
approval:
  required: 2
  allowAgents: true
  allowedAgents: [reviewer, security-scanner]
  excludeAuthor: true
```

Options for the opt-in:

| | Shape | For | Against |
|-|-------|-----|---------|
| (a) | `allowAgents: true` alone admits every registered agent | Short | One line widens a gate to every agent registered now or later |
| (b) | `allowAgents: true` and a non-empty `allowedAgents` list; the CRD requires both together | Two explicit keys; a reviewer sees the switch and the names; no wildcard | Slightly redundant |
| (c) | A non-empty `allowedAgents` alone | One field | Copying a list into a gate silently turns agent approvals on |

**Choice: (b).** CRD rules: `allowAgents` requires a non-empty `allowedAgents`, and
`allowedAgents` requires `allowAgents`. Names are exact AgentIdentity names (at most 32 entries).

Rules:

- An agent approval counts when `allowAgents`, its `actor.agent` is in `allowedAgents`, and it
  is not of the author's party, **whether or not `excludeAuthor` is set** (17-actors §10.3).
  `allowedUsers` and `allowedGroups` do not apply to agents: an agent is admitted by name.
- Otherwise it is recorded with a reason (`agent approvals are off for this gate
  (approval.allowAgents)`, `agent reviewer is not in approval.allowedAgents`, `the author's own
  party`) and does not count.
- The Graph's eligibility filter (`approvalsOfItem` in `pkg/graph/gates.go`) admits an Approval
  when `a.spec.?actor.?kind == 'agent' && allowAgents && a.spec.actor.agent in allowedAgents`,
  in addition to today's user and group test, so allowed agents are copied and others cannot
  crowd out people under the 101-item cap.
- Admission already verifies the Approval's stamp and the agent's `scope.environments`
  (17-actors §5.3), so an agent cannot approve as a person or outside its scope.
- `kardinal approve` run by an agent works as for a person; the CLI stamps it (17-actors §6).

## 6. Agent overrides

A gate override is break-glass: it opens the gate whatever its expression and approvals. An
agent that may override its own autonomy gate has no autonomy limit.

| | Rule | For | Against |
|-|------|-----|---------|
| (a) | Agent overrides count like anyone's, except on its own party's versions | Agents can run incident playbooks | An agent can still override another agent's gate for its sponsor's colleague; the boundary is fuzzy |
| (b) | An override whose actor is an agent never opens a gate | Break-glass stays a human act, simple to state and audit | An agent cannot break glass even when told to |
| (c) | Agent overrides count only on gates that opt in (as approvals) | Flexible | Another opt-in surface for the most dangerous operation |

**Choice: (b).** The override entry stays in the spec (admission verified it), is recorded in
`status.overrides` with `verified` and audited as `GateOverridden` with outcome `Failure` and
the message `overrides by agents are not honoured`, and the gate is evaluated as if it were not
there. An agent that needs a gate opened asks its sponsor (`kardinal why` names who may).

Freeze exemptions (9.3) are break-glass too: an exemption carries its actor stamp, the
`<release>-actor` policy refuses one from an agent, and the controller never honours one stamped
as an agent's (`ExemptionIgnored` on the freeze).

## 7. The `agents:` shorthand

### 7.1 Field

`Pipeline.spec.environments[].agents`: `auto`, `approve` or `deny`, optional. It is the
environment's ceiling for agent versions. Unset means the environment adds no limit of its own,
so the AgentIdentity's autonomy alone applies. The **effective autonomy** is the stricter of
the two (`deny` > `approve` > `auto`, 17-actors §10.4).

| effective | meaning |
|-----------|---------|
| `auto` | the agent's version promotes under the other gates |
| `approve` | a person (or an opted-in agent of another party) must approve the version for this environment |
| `deny` | the agent's version never enters this environment |

`agents` on a fleet environment applies to every target, like a gate's `applies-to`.

### 7.2 Compilation

Options for where the shorthand becomes gates:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | The translator adds one synthetic gate template, `kardinal-agents`, to every environment of a Graph whose Bundle has an agent stamp | Human versions' Graphs are unchanged; no cluster object to manage; the stamp is immutable, so the decision to add it is final | The template exists only in the Graph |
| (b) | The same gate in every Graph, its expression true for non-agents | Uniform | One more PolicyGate per environment per version for every human version; the UI shows an agent gate on every human release |
| (c) | The Pipeline reconciler writes a real PolicyGate template per environment | Visible with `kubectl get policygates` | A second owner of templates; a user can edit or delete it; collection by `applies-to` would match other Pipelines in the namespace |

**Choice: (a).** For an agent Bundle, `translator.Translate` appends, for each environment in
the Graph, a PolicyGate template named `kardinal-agents` in the Pipeline's namespace, labelled
`kardinal.io/scope: system` and `kardinal.io/applies-to: <env>`, with `spec.generated: true`.
It goes through the ordinary path (`matchGatesByEnv`, the `ApprovalGates` or `PolicyGates`
collection), so every gate feature applies: status, reason, audit, PR evidence, `why`, inbox,
the UI, overrides by people. A user template named `kardinal-agents` in a namespace the
translator scans fails the agent Bundle's build with `PolicyGate kardinal-agents is reserved`.

For ceiling `c` (unset is `auto`), the compiled gate is:

| `c` | expression | approval |
|-----|-----------|----------|
| `deny` | `actor.kind != 'agent'` | none |
| `approve` | `actor.kind != 'agent' \|\| (agent.registered && !agent.suspended && agent.autonomy != 'deny')` | `required: 1, excludeAuthor: true, requiredWhen: "actor.kind == 'agent'"` |
| `auto` | the same as `approve` | `required: 1, excludeAuthor: true, requiredWhen: "actor.kind == 'agent' && agent.autonomy == 'approve'"` |

Messages name the cause: `environment prod does not admit agent versions (agents: deny)`;
`agent fixer is suspended, deregistered or denied (AgentIdentity fixer)`;
`an agent's version needs a person's approval in prod (agents: approve)`.

The expressions are correct for any Bundle, so a gate copied into a human version's Graph by
a hand-edit still passes. The approval is excludeAuthor with the one-party rule: neither the
agent, nor its sponsor, nor another agent of the sponsor can open it. An agent of another party
can, only if a team gate opts it in; the shorthand itself never admits agents (no
`allowAgents`). Teams that want specific approvers write their own gate with
`requiredWhen: "actor.kind == 'agent'"` (section 3) next to `agents: auto`.

Re-translation: a change to `agents` is a Pipeline spec change, so in-flight Bundles get the
new gate in place (the #626 mechanism).

## 8. Requiring a stamp

A v0.11 client, kubectl or a GitOps tool may create a version without a stamp (17-actors §5.5).
It reads as `actor.kind: unknown`.

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A gate: `expression: actor.verified` on the environments that need it | Per environment; CEL only; no new API | The version is created, and holds instead of being refused |
| (b) | A chart value making the `<release>-actor` policy refuse unstamped Bundles | Refused at the door | Cluster-wide; every client, GitOps included, must stamp; v0.11 CLIs break |
| (c) | A Pipeline field `requireActor: true` | Discoverable | A field for what one CEL line does |

**Choice: (a)** for v0.12, documented with a ready-made template:

```yaml
spec:
  expression: actor.verified
  message: "this version has no verified actor; create it with kardinal 0.12 or later, or stamp spec.actor"
```

(b) is a follow-up for clusters where every writer is v0.12-aware.

## 9. Agent freeze and stop-the-line

### 9.1 The object

A freeze is a ChangeWindow with `spec.freeze`. It holds promotions by itself, without a gate
naming it:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeWindow
metadata:
  name: kardinal-freeze-agents
spec:
  type: blackout
  start: "2026-10-14T09:00:00Z"
  # end omitted: until it is removed (kardinal unfreeze)
  reason: "INC-4211: agents paused while we investigate"
  freeze:
    actors: agents             # agents | everyone
    namespaces: []             # exact names or "prefix*"; empty = every namespace
    createdBy: alice@example.com   # verified by admission
    exemptions:                # break-glass, one per version or environment
      - namespace: payments
        pipeline: checkout
        environment: prod      # optional; "" = every environment of the pipeline
        bundle: checkout-7f3c2a  # optional; "" = any version
        reason: "hotfix for INC-4211"
        expiresAt: "2026-10-14T12:00:00Z"
        createdBy: bob@example.com # verified by admission
```

- `actors: agents` holds versions whose stamp says `kind: agent`. `actors: everyone` holds every
  version (stop-the-line, Kargo #1422). A version without a stamp is not an agent's: agents must
  stamp (17-actors §5.5).
- A freeze window is a blackout whose `end` may be omitted. The schema rule becomes "a blackout
  requires start, and end unless it is a freeze". `changewindow.Evaluate` treats a missing end
  as "active from start on". A freeze can be named in CEL like any window.
- The ChangeWindow reconciler, which already owns the clock for windows, writes everything
  time-based a freeze needs to its own status: `status.active`, `status.exemptions` (the
  exemptions not yet expired), `status.nextTransition` (the next window boundary or exemption
  expiry, when it requeues itself) and `status.observedGeneration`.
- Name convention for the CLI: `kardinal-freeze-<agents|everyone>[-<namespace>]`, so
  `kardinal freeze` is idempotent per scope.

### 9.2 Where it is enforced

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | The translator adds a synthetic freeze gate to every environment of every Graph | Gate tooling for free (status, why, UI, overrides) | One more PolicyGate per environment per version, always, for a rare event; turns on the gate mirror for every `pr-review` step; Graphs built before the upgrade have none; a gate override in the namespace would bypass a cluster stop-the-line |
| (b) | Every Graph gets a selector `ref` on freeze ChangeWindows, and the step nodes' conditions read it | Graph-native | The Graph identity needs a cluster-wide list/watch (G18/G19); kro CEL has no clock for start, end and exemption expiry (G8); only holds steps not yet created; Graphs built before the upgrade have none |
| (c) | The PromotionStep reconciler holds a step while a matching freeze is active, at the two safe points where it holds a paused pipeline (`holdIfPaused`) | Covers every step, every Graph, including those built before the upgrade; no objects added; the same semantics as pause, which users know | Logic outside the Graph (the ledger's existing "Holding an existing PromotionStep" row) |

**Choice: (c).** A kill switch must cover every in-flight promotion at once, including Graphs
built before it existed and steps already created; (a) and (b) cannot. The mechanism:

- `holdIfFrozen(ctx, step)` runs after `holdIfPaused` before a Pending step starts and before a
  Promoting step's next git step. Steps waiting for a PR merge or running a health check carry on,
  as with pause: stopping them would leave a merged change unverified.
- It reads no clock. It lists ChangeWindows from the cache (no API call) and keeps those with
  `spec.freeze` whose status says active (a status behind the spec's generation counts as
  active, so a new freeze holds at once and an edit fails closed), whose `namespaces` match the
  step's namespace (`actor.Matches`), and whose `actors` match the version (`everyone`, or
  `agents` and the Bundle's `spec.actor.kind == agent`). The Bundle is read only when an agent
  freeze exists.
- An exemption lets a step through when it is in the window's `status.exemptions` (so not
  expired, as its reconciler decided) and still in the spec (a removed exemption ends at once),
  and namespace and pipeline equal, `environment` is empty or equal, `bundle` is empty or equal.
- Held: the step writes only its own `status.message`,
  `frozen by kardinal-freeze-agents (agents, every namespace) since 09:00 UTC by alice@example.com:
  INC-4211 — kardinal unfreeze kardinal-freeze-agents, or kardinal override checkout prod --freeze --reason <why>`,
  and requeues after one minute as a fallback.
- Auto-merge is off while a step is frozen (`autoMergeBlocked` gains the freeze check).
- `SetupWithManager` watches ChangeWindows with `spec.freeze` and wakes the Pending and
  Promoting steps in the matching namespaces, so a freeze, an unfreeze, a new exemption and an
  expiry (the window reconciler's status write at `nextTransition`) take effect within one step
  reconcile; the one-minute requeue is the fallback.
- An exempted step proceeds and writes one `FreezeExempted` AuditEvent (outbox in the step's
  status, as its other audit records), named from the step, the window and the exemption, so a
  re-run writes no second one. It records the exemption's `createdBy`, reason and expiry.

`kardinal why`, `kardinal status`, the UI's stage drawer and PR evidence already show a held
step's message.

### 9.3 The override

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A PolicyGate override on a synthetic freeze gate | Existing command | Needs 9.2 (a); anyone who may override gates in the namespace bypasses a cluster freeze |
| (b) | `spec.freeze.exemptions` on the ChangeWindow, verified by admission, used and audited by the step | Only someone allowed to edit the freeze can make an exception; one record per exception, with who and why | A new admission policy |
| (c) | Edit the freeze (narrow `namespaces`, or unfreeze and refreeze) | Nothing new | Not per version or environment; leaves no record of who let what through |

**Choice: (b).** Breaking a cluster-wide freeze must take the authority that set it.
`kardinal override <pipeline> <env> --freeze --reason <why> [--expires-in 1h] [--bundle <name>]`
adds an exemption to every active freeze that holds that environment, as the caller.

The `<release>-freezes` ValidatingAdmissionPolicy matches CREATE and UPDATE of
`changewindows`:

- On CREATE with `spec.freeze`: `freeze.createdBy == request.userInfo.username`.
- On UPDATE: `freeze.createdBy` unchanged; `spec.freeze` cannot be added or removed; every
  exemption that is not in `oldObject` has `createdBy == request.userInfo.username`, and no
  existing exemption changes (removal is allowed: that ends it early).
- This release's controller and `admission.controllerUsernames` are exempt: the UI API writes for
  callers it authenticated (TokenReview), with the caller as `createdBy`.

Written with the same CEL constructs as the `<release>-gate-overrides` policy, which runs on
every Kubernetes kardinal supports (1.35 and later).

An exemption is bounded as a gate override is: the ChangeWindow reconciler records when it first
saw each entry (`status.exemptionRecords`, keyed by a hash of the entry) and counts it until the
earlier of its `expiresAt` and that time plus `--freeze-exemption-max-minutes` (Helm
`controller.freezeExemptionMaxMinutes`, a day by default). The step reads the record's end, so
`--expires-in 8760h` lets a pipeline through for a day, not a year. Each new exemption also
carries its writer's actor stamp, checked by the `<release>-actor` policy (section 6: an agent's
is refused and never honoured).

### 9.4 CLI

```
kardinal freeze --agents   [--cluster | -n <ns>] --reason <why> [--for <duration>]
kardinal freeze --everyone [--cluster | -n <ns>] --reason <why> [--for <duration>]
kardinal unfreeze <name> | --agents|--everyone [--cluster | -n <ns>]
kardinal freeze                                   # list active freezes
kardinal override <pipeline> <env> --freeze --reason <why> [--expires-in <d>] [--bundle <name>]
```

`--cluster` is the default scope for `freeze`. `--for` sets `end`; without it the freeze lasts
until `unfreeze`. A repeated `freeze` updates the reason and end of the existing window.
`unfreeze` deletes the window; it never deletes a ChangeWindow without `spec.freeze`.

RBAC: freezing is creating and deleting cluster-scoped ChangeWindows. The chart's user roles do
not include it; the docs show a `ClusterRole` for incident commanders
(`create, update, patch, delete` on `changewindows`).

### 9.5 UI

- A banner on every page while a freeze is active: who froze, which actors, which namespaces,
  since when, the reason, and **Unfreeze** for users who may.
- **Freeze agents** and **Stop the line** in the top bar's menu open a dialog asking for a reason
  and an optional duration; **Stop the line** asks to confirm by typing `stop`.
- UI API: `GET /api/v1/ui/freezes`, `POST /api/v1/ui/freeze`, `POST /api/v1/ui/unfreeze`.
  In TokenReview mode the controller checks the caller with a SubjectAccessReview for the
  virtual action `update changewindows/freeze` (cluster scope, or the namespace a freeze is
  limited to) and writes the window as itself, with the caller as `createdBy`. The namespace's
  right covers only the caller's own freezes of that namespace: changing (re-freezing over) or
  lifting a freeze someone else made needs `update changewindows/freeze` cluster-wide, else 403.
  Otherwise a team's admin, which the chart's admin role lets freeze its namespace, could lift an
  incident commander's stop-the-line of it through the UI, which it cannot do with kubectl
  (ChangeWindows are cluster-scoped). Without
  TokenReview the endpoints are refused: a freeze names who froze. The writes are opt-in, Helm
  `ui.freezeControls` (`--ui-freeze-controls`, which needs TokenReview): only then does the chart
  grant the controller `create, update, delete` on `changewindows`, and the freezes policy admits
  the controller's writes only with `spec.freeze`. Off, the UI shows freezes and `kardinal freeze`
  writes them.
- A held step's drawer shows the step message (9.2).

### 9.6 Writes by agents during a freeze

An agent freeze holds agent versions; it does not refuse an agent's writes. Suspending an
AgentIdentity (17-actors §3) refuses every write by that agent. Refusing all agents' writes
during a freeze would need the `<release>-actor` policy to read the freeze through the
AgentIndex params: a follow-up.

## 10. Graph-first

| Item | Graph-first question | Mechanism | Ledger |
|------|----------------------|-----------|--------|
| `actor.*` | (3) CEL variable | From `bundle.spec.actor`, read by the PolicyGate reconciler like `bundle.*` | G8 PolicyGate CEL row |
| `agent.*` | (3) CEL variable | PolicyGate reconciler reads the AgentIdentity (2.2) | G8 cross-CRD reads row gains AgentIdentities; the Graph-native path is blocked on G18/G19 |
| `requiredWhen`, one-party, agent approvals, agent overrides | (2) owned node | The gate instance's reconciler writes `status.ready` from its own spec | none |
| `agents:` | (2) owned node | Translator-generated gate template, ordinary gate instances | none |
| freeze | (2) owned node + outside the Graph | The ChangeWindow reconciler writes the time-based state to its own status; the PromotionStep reconciler holds on it, like pause | G8 "Holding an existing PromotionStep" row gains freezes |
| freeze admission | VAP | `<release>-freezes` | none |

## 11. What this does not protect against

- **An unregistered agent is a human or a bot** (17-actors §11). `agents:` and agent freezes see
  only registered agents. Gate prod on `actor.verified && actor.kind == 'human'` and a trusted
  group to hold back what nobody registered.
- **A person who may edit the Pipeline** can set `agents: auto`; the AgentIdentity's autonomy,
  set by cluster admins, still bounds it.
- **Whoever may edit freeze ChangeWindows** can unfreeze or exempt. That is the authority by
  design; the exemption records who.
- **Steps already waiting for a merge or a health check** are not stopped by a freeze, as with
  pause. Close the PR to stop one.
- **Index lag** (17-actors §11) delays a suspension at admission; the gate sees it at its next
  evaluation, woken by the AgentIdentity watch.

## 12. Upgrade from v0.11.0

- No version has a stamp, so every `actor.kind` is `unknown` and no `kardinal-agents` gate is
  added. `excludeAuthor` compares principals as before: `party(unknown) = {principal}`.
- The new fields (`approval.requiredWhen`, `allowAgents`, `allowedAgents`,
  `status.approvalRequired`, `environments[].agents`, `ChangeWindow.spec.freeze`) are optional.
- Graphs built by v0.11 carry no `requiredWhen` and keep their behaviour; a freeze holds their
  steps too (9.2 (c)).
- The CRDs change and are applied before the chart, as every upgrade requires. The live
  upgrade test creates a v0.11 version, upgrades, freezes everyone and checks that it holds.

## 13. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| B1 | Source of `agent.*` | ref node (17-actors A7); translator snapshot + re-translate; PolicyGate reconciler reads AgentIdentity | **Reconciler reads it** (2.2): no Graph RBAC widening (G18/G19), no schema field, a suspension reaches the gate in one reconcile. Amends A7 |
| B2 | Conditional approval | `approval.requiredWhen`; native ceiling field; translation-time snapshot | **`requiredWhen`** (3): CEL only, general, live |
| B3 | Agent approval opt-in | `allowAgents` alone; both keys required; `allowedAgents` alone | **Both keys** (5): two explicit keys, names only |
| B4 | Agent overrides | as anyone's except own party; never; opt-in | **Never** (6): break-glass is a human act |
| B5 | Where `agents:` compiles | synthetic template for agent Bundles; for every Bundle; a real template | **Synthetic, agent Bundles only** (7.2): human Graphs unchanged, every gate feature applies |
| B6 | Requiring a stamp | CEL gate; chart policy; Pipeline field | **CEL gate** (8), the chart policy later |
| B7 | Freeze enforcement | synthetic gate everywhere; Graph selector ref; PromotionStep hold | **PromotionStep hold** (9.2): covers every in-flight step and Graph, no objects added. It reads the ChangeWindow's status (active, exemptions in effect), which the window reconciler writes at each boundary: no clock in the step |
| B8 | Freeze override | gate override; ChangeWindow exemptions; edit the freeze | **Exemptions** (9.3): only the freeze's authority breaks it, each exception recorded |
| B9 | Open-ended freeze | require end; default duration; optional end for freezes | **Optional end** (9.1): a stop-the-line lasts until it is lifted |
| B10 | Votes per party | per principal; per party key | **Per party key** (4): alice and her agent are one approval |

## 14. Test plan

**Unit** (table-driven, `-race`):

- `buildContext`: `actor.*` for each kind and for no stamp; `agent.*` for registered, not ready,
  suspended, missing and non-agent; the AgentIdentity read only when referenced; read errors
  fail closed.
- `requiredWhen`: true, false, error; `approvals.met`; `status.approvalRequired`; standing.
- `TallyApprovals`: the one-party table (author × approver over human, agent, sponsor, sibling
  agent, unknown), party-key counting, reject eligibility, agent opt-in (off, on and listed, on
  and not listed, own party), held cases.
- Graph: `approvalsOfItem` admits allowed agents and drops others; the policy data round-trips.
- Translator: `kardinal-agents` added for agent Bundles only, per ceiling, reserved name, fleet.
- Freeze: `changewindow.Evaluate` without end; the ChangeWindow reconciler's freeze status
  (exemptions in effect, `nextTransition`, idempotency); `holdIfFrozen` over actors × namespaces ×
  exemptions × a status behind its spec; audit idempotency; auto-merge off.
- Admission: the freezes policy rendered and evaluated in `test/helm` for create, forged
  `createdBy`, a new exemption by its author and by someone else, changed exemption, removed
  exemption, controller exemption.
- CLI: `freeze`, `unfreeze`, `override --freeze`, `policy simulate --as-agent`.

**Fake-client journeys** (`test/e2e/journeys_test.go`: the real reconcilers and CEL on a fake client):

1. An agent version auto-promotes to test (`agents: auto`, autonomy `auto`).
2. An agent version waits for a person's approval in prod (`agents: approve`), and passes once
   a person of another party approves.
3. Agents are denied in prod (`agents: deny`): the version stops before prod.
4. An agent approval counts only on a gate with `allowAgents` and the agent listed.
5. An agent cannot approve its own version or its sponsor's; the sponsor cannot approve the agent's.
6. An agent freeze holds an agent version and not a person's.
7. Stop-the-line holds everyone's.
8. An exemption during a freeze lets one environment through and writes `FreezeExempted`.

**Live e2e** on kind (`KIND_CLUSTER=kp-policy`, COUNT=2, rows in `test/e2e/coverage.tsv`): the
eight journeys against a real API server, controller, kro and admission, plus the freezes
policy's denials and the UI API freeze endpoints.
