# Agents and the verified actor

Every version, approval and gate override kardinal records says **who made it, and for whom**:
the *actor*. A person, a CI bot, kardinal itself, or an AI agent with the person who answers
for it. The API server verifies the actor when the record is written, so nobody can write a
record in someone else's name, and an agent cannot pass as a person.

```text
$ kardinal history checkout
VERSION          ACTION    ACTOR                          ENV    PR    DURATION   TIMESTAMP
1.5.0 (ck-7f3)   promote   agent fixer for alice          uat    #42   ...        2026-10-14 09:12
1.4.0 (ck-2a1)   promote   bob                            prod   #40   4m         2026-10-13 16:03
1.3.2 (ck-9d0)   promote   kardinal (subscription)        test   --    1m         2026-10-13 11:47
```

## The actor

| Field | Verified | What it is |
|---|---|---|
| `kind` | yes | `human`, `agent`, `bot` (a ServiceAccount that is not a registered agent, or the Bundle API's static token) or `controller` (kardinal acting on its own) |
| `principal` | yes | The username the API server authenticated |
| `agent` | yes | The agent's name (its AgentIdentity), when `kind` is `agent` |
| `sponsor` | yes | The person who answers for the agent, when `kind` is `agent`, as registered when the record was written |
| `via` | yes | `ui` or `api` when kardinal wrote the record for a caller it authenticated; `subscription` or `auto-rollback` for its own versions |
| `asserted.model`, `asserted.session`, `asserted.client` | **no** | What the client says about itself. Recorded as written, shown labelled "asserted", never used to decide anything |

The actor is in each record's own spec, and never changes after the record is written:

| Record | Field |
|---|---|
| Version (Bundle) | `spec.actor` |
| Approval | `spec.actor` |
| Gate override | `spec.overrides[].actor` |
| AuditEvent | `spec.actor` |

A record written before v0.12, or with `kubectl` without a stamp, has no actor. kardinal shows
it by its `kardinal.io/created-by` (or the Approval's `spec.user`), and its kind is `unknown`.

## How the actor is verified

The chart's `<release>-actor` ValidatingAdmissionPolicy checks every new version, Approval and
gate override. It reads the agent registry from the `AgentIndex` named after the release, which
the controller builds from the AgentIdentities, and admits a stamp only when it says exactly
what the requester is:

- the `principal` is the requester's username;
- the `kind` is `agent` when the requester, or one of its groups, is a registered agent, `bot`
  for any other ServiceAccount, and `human` otherwise;
- an agent's `agent` and `sponsor` are the ones registered;
- only kardinal's controller sets `via`.

An agent must stamp what it writes; a person or a bot may leave the stamp out, as before
v0.12. An agent that is suspended, or out of its scope, is refused. `kardinal` sets the stamp
for you: `create bundle`, `ship`, `promote`, `rollback`, `approve` and `override` read your
identity and the agent index, and refuse an agent out of its scope before writing anything.
The UI and the Bundle API stamp for the caller they authenticated, with the same rules.

A refusal says what to write:

```text
$ kubectl --as=system:serviceaccount:ci:fixer create -f bundle.yaml
Error from server (Forbidden): ... you are agent fixer (sponsor alice@example.com): set actor
{kind: agent, principal: system:serviceaccount:ci:fixer, agent: fixer, sponsor: alice@example.com};
kardinal sets it for you
```

To record what the client asserts about itself, set it in the environment of the `kardinal`
command:

```bash
export KARDINAL_ACTOR_MODEL=claude-opus-5 KARDINAL_ACTOR_SESSION=run-7f3c2a
kardinal ship checkout --image ghcr.io/acme/checkout:1.5.0
```

`KARDINAL_ACTOR_CLIENT` names the tool. Unset variables record nothing.

## Register an agent

An `AgentIdentity` is cluster-scoped: only cluster-wide admins write it (cluster admins, and
holders of `<release>-admin` through a ClusterRoleBinding).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
metadata:
  name: fixer
spec:
  description: Fixes failing builds and ships the fixes to test.
  subjects:                     # who is this agent: 1 to 16
    - kind: ServiceAccount
      namespace: ci
      name: fixer
    - kind: Group               # every member of the group is this agent
      name: oidc:agents-fixer
  sponsor:
    user: alice@example.com     # a person: never a ServiceAccount
    team: payments              # shown, not used by policy
  scope:
    namespaces: ["payments-*"]  # exact names, or a prefix ending in "*"; empty: nowhere
    pipelines: ["checkout"]     # versions only for these Pipelines; empty: all
    environments: ["test", "uat"]  # approvals and overrides only here; empty: all
  autonomy: approve             # auto | approve | deny: the most the agent may do alone
```

```text
$ kubectl get agentidentities
NAME    SPONSOR             AUTONOMY   SUSPENDED   READY   AGE
fixer   alice@example.com   approve    false       true    2m
```

The AgentIdentity is ready unless it is invalid:

- **SubjectConflict**: another AgentIdentity already lists one of its subjects. The older one
  keeps it: a principal is never two agents.
- **InvalidSponsor**: its sponsor is itself an agent.
- **IndexFull**: the agent index is at its size limit (about 4,000 subjects).

The subjects of an AgentIdentity that is not ready are refused, with its reason, rather than
treated as people. `autonomy` is recorded and shown; gates that act on it are planned
([roadmap](roadmap.md)).

Registering an agent decides how kardinal *records* it. What it may *do* is still decided by
RBAC: give its ServiceAccount the chart's roles as for anyone else
([User roles](guides/security.md#user-roles)).

### Suspend or remove an agent

Set `spec.suspended: true` to stop an agent at once: every write by its subjects that the actor
policy checks is refused, and nothing it already wrote changes. The change takes effect within
seconds, when the controller rewrites the index.

An AgentIdentity can be deleted only once it is suspended. Deleting it turns its ServiceAccount
subjects into bots and its users and groups into people, so remove their RBAC first.

### Who can read the registry

Every authenticated user can read AgentIdentities and the AgentIndex
(`actors.directory.public: true`, the default): `kardinal` reads the index to stamp what you
write, and the UI shows who sponsors an agent anyway. With `false`, bind the
`<release>-agent-directory-reader` ClusterRole to the people and agents who use kardinal; a
caller who cannot read the index writes no stamp, which is refused for an agent.

## Where the actor shows

- **CLI**: `kardinal history` has an `ACTOR` column, and `kardinal why` names who made the
  version waiting. `-o json` carries the `actor` object.
- **UI**: the stage drawer says who shipped the version ("Shipped by agent fixer for
  alice@example.com"), with the asserted model apart.
- **PR evidence**: the "Created by" line is the actor
  ([Read PR evidence](pr-evidence.md#artifact-provenance)).
- **Audit**: each AuditEvent names its actor, and every write through the UI API and the Bundle
  API is an `APIWrite` AuditEvent naming the caller and the route. `kardinal audit export`
  writes them as JSON lines or OCSF events for a SIEM
  ([Audit logging](guides/security.md#audit-logging)).

## What this does not protect against

- A stolen agent credential is the agent: suspend the AgentIdentity and revoke the credential.
- An agent nobody registered is a person (an OIDC user) or a bot (a ServiceAccount).
- Asserted fields are claims.
- The index is rebuilt after each AgentIdentity change; while the controller is down, the index
  is stale but stays in place.
- Anyone with `edit` or `admin` in the controller's namespace can act as the controller.

The design, with the options considered for each decision, is in
[Actors](design/17-actors.md).
