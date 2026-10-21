# Agent budgets

An agent with `autonomy: auto` promotes on its own as often as its scope allows. An agent
budget limits how much it changes before a person looks: versions a day, promotions at once,
fleet targets an hour, failures left in place, and a share of a fleet. A step of the agent's
version beyond a limit **waits** in Pending, with a message that names the limit; it is never
failed for its budget, and it starts as soon as there is room. People's versions, bots' and the
controller's are never held.

Budgets are off by default. The design, with its trade-offs and what it does not protect
against, is [24-agent-budgets.md](design/24-agent-budgets.md).

## Turn budgets on

Budgets are counted per installation. Turn them on in the release's values:

```yaml
actors:
  budgets:
    enabled: true
```

With `actors.budgets.enabled: false` (the default), budgets and exemptions are kept on the
AgentIdentity but nothing enforces them, and a write that sets them gets a warning:
`agent budgets are off in this release (actors.budgets.enabled): they are kept but not
enforced`. `kardinal doctor` lists the agents whose budgets are not enforced.

## Set a budget

Budgets are rules on the agent's AgentIdentity. Every rule that matches a step applies:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
metadata:
  name: fixer
spec:
  subjects:
    - kind: ServiceAccount
      namespace: ci
      name: fixer
  sponsor:
    user: alice
  scope:
    namespaces: ["payments-*"]
  autonomy: auto
  budgets:
    - name: prod
      environments: ["prod", "prod-*"]  # exact names, or a prefix ending in "*"
      versionsPerDay: 5                 # versions started into these environments in any 24 hours
      maxConcurrent: 2                  # promotions under way at once
      fleetTargetsPerHour: 40           # fleet targets started in any hour
      maxUnavailable: 1                 # failed promotions whose environment still runs the failed version
      fleetShare: "25%"                 # of one fleet's targets, per version (rounded up, at least one)
    - name: pre-prod
      environments: ["uat", "staging"]
      namespaces: ["payments-*"]        # default: every namespace
      versionsPerDay: 50
```

| Limit | Counts | Window |
|---|---|---|
| `versionsPerDay` | a version once per environment; a fleet's targets are one environment | the last 24 hours |
| `maxConcurrent` | promotions granted and not yet Verified, Superseded, Failed or rolled back | now |
| `fleetTargetsPerHour` | fleet targets started; plain environments do not count | the last hour |
| `maxUnavailable` | promotions that failed while their environment still runs the failed version; a newer version there ends it | now |
| `fleetShare` | one version's targets of one fleet under way or failed, against the fleet's current size | now |

A rule sets at least one limit, at most 16 rules per agent, and the counted limits of all
rules sum to at most 1,500. The windows roll: a version counts until it is 24 hours old, not
until midnight. Only an admin with the `manage` verb on AgentIdentities changes budgets
(cluster admins, and `<release>-admin`).

A budget added or tightened applies at once to versions that have not started; promotions
already under way keep running, and count. When budgets are turned on, or a rule added, the
controller counts the promotions of the agent that started within the window and still have
steps.

## See a hold

A held step says why, without counts, so it is not rewritten while counts move:

```bash
kardinal why my-app prod
```

```
prod waits for agent fixer's budget.
  Version  1.7 (my-app-7f3c2a), by agent fixer for alice
  Why      waiting for agent fixer's budget (rule prod: versionsPerDay); kardinal budget fixer
  Next     kardinal budget fixer
  Or       kardinal override my-app prod --budget --reason <why>, by someone who may exempt the agent
```

A held version goes on by itself: when one of the agent's granted promotions settles
(`maxConcurrent`), when the window's oldest grant ages out (`FREES AT` below), or through an
exemption. A newer version of the Pipeline supersedes a held one as it does any version that has
not started: the held step fails before it starts ("superseded before this step started") and
leaves the queue without using the budget.

`kardinal status my-app` shows the environment as `Held by agent fixer's budget`. The counts:

```bash
kardinal budget fixer
```

```
Agent fixer (sponsor alice): 2 budget rule(s).

Ledger kardinal-promoter (evaluated 2026-10-15T10:02:11Z; current)
  RULE      LIMIT                USED   WINDOW  FREES AT              WAITING
  prod      versionsPerDay       5/5    24h     2026-10-15T10:41:00Z  1        exhausted
  prod      maxConcurrent        1/2    -       -                     1
  pre-prod  versionsPerDay       12/50  24h     2026-10-15T09:14:30Z  0

Waiting:
  payments/my-app  prod  my-app-7f3c2a  since 2026-10-15T10:01:58Z  rules prod
```

`-o json` prints the ledgers (the AgentIdentity's `status.usage`). The waiting versions are
listed when you may list PromotionSteps in every namespace. The controller also emits
`BudgetExhausted` and `BudgetAvailable` events on the AgentIdentity when a rule runs out of
room or has room again.

A gate can read the budget too, for more scrutiny as the agent uses its day (see
[`budget.*`](reference/cel-context.md#budget)): a gate shows a budget, the grant enforces it.

## Let one version through: exemptions

In an incident, a person lets one environment, or one version, through the agent's budget:

```bash
kardinal override my-app prod --budget --reason "hotfix for INC-4211" --bundle my-app-7f3c2a
```

The exemption is added to the agent's AgentIdentity as you, for an hour by default
(`--expires-in`), and counts for at most a day from when the controller first sees it. The
step that goes on under it writes one `BudgetExempted` AuditEvent naming you, why and until
when. An exempt promotion still counts, so later versions see the true usage.

Who may: bind the `<release>-agent-budget-exempter` ClusterRole (the `exempt` verb, with
`patch`) to your incident commanders. The admission policies refuse an exemption made by the
agent's sponsor or one of its subjects, one by any agent (break-glass is for people), one whose
`createdBy` or actor stamp is not yours, and any change to an existing exemption; removing one
is allowed. An exemption the controller could not verify (made by an agent, without an actor
stamp, or while the admission policies are not installed) never counts: the ledger lists it as
ignored and says why.

## Shards and several installations

The controller counts each agent in a **ledger**, the AgentIdentity's `status.usage.<ledger>`,
written by one controller per installation:

- **A sharded installation** (several releases of one installation, [sharding](sharding.md)) shares
  one ledger: set `actors.budgets.enabled` and `actors.budgets.ledger` alike on every shard,
  with the ledger set to the default shard's release. The default shard's controller grants;
  the other shards' steps read its grants. A shard whose ledger names a release that does not
  count holds its agents' steps, and `kardinal doctor` names the ledger nobody writes.
- **Separate installations** (a release per team with `controller.watchNamespace`) each count
  only what they see, in their own ledger: an agent that works through two of them has twice
  the budget. Set budgets with that in mind.

An AgentIdentity holds at most 16 ledgers, and 3,000 grants between them.

## Limits of budgets

A budget limits the promotions kardinal makes for an agent that admission recognised: an
unregistered agent is a person or a bot, and an agent with direct write access to the GitOps
repository or the cluster is not limited by it. It counts versions and targets, not the size of
a change. People can always ship the same fix themselves.

## Upgrade

Every field is optional and budgets are off, so the upgrade changes nothing until you turn
them on. Apply the CRDs before the chart, as every upgrade does: AgentIdentity
(`spec.budgets`, `spec.budgetExemptions`, `status.usage`), PromotionStep (`status.budget`),
Pipeline (`status.environments[].budgetHeldBy`) and AuditEvent (the `BudgetExempted` action).
