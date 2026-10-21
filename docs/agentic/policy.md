# Actor-aware policy

!!! info "New in v0.12.0"
    v0.11.0 and earlier do not have this. The [changelog](../changelog.md) lists every v0.12.0 change.

kardinal records who created each version, and for an agent which person answers for it
([Actors](../actors.md)). Policy uses that record: an agent can ship to test on its
own and wait for a person in prod, its sponsor cannot approve its work, and agents' approvals
count only where you say so. The controller enforces all of it, so an agent cannot talk its
way past it.

## Autonomy per environment

Set `agents` on an environment to limit what an agent's version may do there:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: checkout
spec:
  environments:
    - name: test
      agents: auto       # agents' versions promote under the other gates
    - name: uat          # unset: the AgentIdentity's own autonomy decides
    - name: prod
      agents: approve    # a person approves each agent's version (auto | approve | deny)
```

| `agents` | An agent's version in that environment |
|---|---|
| `auto` or unset | No limit of its own: the AgentIdentity's `autonomy` decides, and its default, `approve`, still waits for a person |
| `approve` | Waits for an approval from a person who is not of the agent's party (not its sponsor) |
| `deny` | Never enters it |

The agent's AgentIdentity has its own ceiling, `spec.autonomy` (default `approve`), set by
cluster admins. The stricter of the two applies, so neither the Pipeline owner nor the agent's
admin can loosen the other's limit. An agent that is suspended, deregistered, or whose
autonomy is `deny` is held in every environment, including versions already on their way.

kardinal adds one gate, `kardinal-agents-<ceiling>`, to every environment of a version an agent
created, so the wait shows like any other gate: in `kardinal why`, the inbox, the UI and the PR
evidence. Versions people create get no such gate, except those stamped `unverified`, written
while the agent index wasn't Ready: they count as an agent's, though a rollback to what was
Verified in the environment passes
([While the index isn't Ready](../actors.md#while-the-index-isnt-ready)). A PolicyGate of your
own may not be named `kardinal-agents-*`.

## One party, one vote

An agent and its sponsor count as one party. With `excludeAuthor: true` on an approval gate:

- nobody of the author's party may approve: not the author, not the author's agent, not the
  sponsor of the agent that created the version, and not another agent of that sponsor;
- `required: N` counts parties: alice and her agent are one approval.

So an agent cannot approve its own work, and its sponsor cannot approve it for it. See
[One party, one vote](../approvals.md#one-party-one-vote).

## Agents as approvers (opt-in)

Agents' approvals count nowhere unless a gate opts in, by name:

```yaml
approval:
  required: 2
  allowAgents: true
  allowedAgents: [security-reviewer]
  excludeAuthor: true
```

An agent's approval never counts on work of its own party, whatever `excludeAuthor` says. See
[Agents as approvers](../approvals.md#agents-as-approvers).

## Approval for agents only

`approval.requiredWhen` applies an approval policy only while a CEL condition holds. Your own
approvers for agents' versions, with people shipping on their own:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: agents-need-a-release-manager
  namespace: checkout
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "true"
  message: "an agent's version needs a release manager in prod"
  approval:
    required: 1
    allowedGroups: [release-managers]
    excludeAuthor: true
    requiredWhen: "actor.kind == 'agent'"
```

## Rules on the actor

Every gate expression can read `actor.*` (who created the version, fixed when it was created)
and `agent.*` (the agent's registration now); see the
[CEL context](../reference/cel-context.md#actor). Two common rules:

```yaml
# Refuse versions without a verified actor (v0.11 clients, hand-written Bundles)
expression: actor.verified
message: "this version has no verified actor; create it with kardinal 0.12 or later"
---
# People only in prod
expression: actor.verified && actor.kind == "human"
```

A version without a stamp reads as kind `unknown` with its `kardinal.io/created-by` as the
principal. Agents must stamp: admission refuses an agent's write without one.

## Ask before you ship

`kardinal policy simulate` evaluates an environment's gates for a version an agent would create,
with the agent's registration as it is now:

```bash
kardinal policy simulate checkout prod --as-agent fixer
kardinal policy simulate checkout prod --as-human alice@example.com
```

## Break-glass is for people

A gate override by an agent never opens a gate. It is recorded, named in the gate's
`OverrideIgnored` condition and audited as not honoured. An agent that needs a gate opened asks
a person; `kardinal why` says who may.

## What this does not cover

- An agent nobody registered is a person or a bot to kardinal. Gate prod on
  `actor.verified && actor.kind == "human"` and a trusted group to hold back what nobody
  registered.
- Whoever may edit the Pipeline may set `agents: auto`; the AgentIdentity's autonomy still
  bounds it.

The design and its decisions: [18-actor-policy](../design/18-actor-policy.md).
