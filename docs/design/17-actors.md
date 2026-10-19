# Design: Actors (who acted, and for whom)

> Status: Implemented in v0.12.0 (epic #1834, #1857).
> Created: 2026-10-14
> Absorbs: #1539 (AuditEvents for UI API and Bundle API writes).
> Consumers: E7 actor-aware policy and separation of duties, E8 Verdicts, E9 freezes, E10 local MCP.
> Verified against: Kubernetes `k8s.io/apiserver` v0.37.1 (the version in `go.mod`) and kro
> v0.10.0-rc.0 (`54a203b`). Source references are `path:line` in those trees.

---

## 1. Problem

Today kardinal records one verified name for each write that names a person:

| Record | Field | How it is verified |
|--------|-------|--------------------|
| Bundle | annotation `kardinal.io/created-by` | `<release>-bundle-creator` policy: equals `request.userInfo.username` |
| Approval | `spec.user`, `spec.groups` | `<release>-approvals` policy: the user is the requester, the groups are among the requester's |
| Gate override | `spec.overrides[].createdBy` | `<release>-gate-overrides` policy |
| Rejection | `spec.rejected.by` | `<release>-bundle-rejection` policy |

That name is a Kubernetes principal. It cannot say whether the principal is a person, a CI job
or an AI agent, and for an agent it cannot say which person answers for it. Policy therefore
cannot treat agents differently ("agents may ship to test, not to prod"), and separation of
duties is wrong for agents: an agent's sponsor can approve the agent's work, because the two
names differ.

This spec defines:

- `AgentIdentity`, the registry entry that turns a principal into an agent with a sponsor;
- the **actor**: a structured, verified record of who acted (kind, principal, agent, sponsor),
  stamped on every Bundle, Approval and override;
- how admission verifies the actor without being able to look up arbitrary objects;
- `AuditEvent.spec.actor`, AuditEvents for UI API and Bundle API writes, and
  `kardinal audit export`;
- how the actor is shown, and how later epics consume it (CEL variables, the one-party rule).

## 2. Terms

| Term | Meaning |
|------|---------|
| **Principal** | The username the API server authenticated (`request.userInfo.username`). Always verified. |
| **Agent** | A principal registered in an `AgentIdentity`. The agent's name is the `AgentIdentity` name. |
| **Sponsor** | The person accountable for an agent: `AgentIdentity.spec.sponsor`. Required. |
| **Actor** | The record of who made a change: kind, principal, agent, sponsor, plus asserted fields. |
| **Party** | The set of identities that count as one for separation of duties (section 10.3). |
| **Verified** | Checked by the API server's admission or computed by the controller from verified inputs. A policy may rely on it. |
| **Asserted** | Written by the client and recorded as written. Shown, exported, matchable by CEL, never trusted for a security decision. |

## 3. AgentIdentity

A cluster-scoped CRD (`agentidentities.kardinal.io`, short name `agentid`). Cluster admins
register each agent. It is cluster-scoped because an agent's principal is cluster-wide (an OIDC
user, or a ServiceAccount that can be granted rights in any namespace) and because a tenant must
not be able to register, re-sponsor or widen an agent by writing in its own namespace.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
metadata:
  name: fixer                    # the agent's name in every actor record
spec:
  description: Fixes failing builds and opens Bundles for the fixes.
  subjects:                      # who this agent is; at least one, at most 16
    - kind: ServiceAccount
      namespace: ci
      name: fixer
    - kind: User                 # an OIDC (or any) username, exactly as authenticated
      name: oidc:fixer-bot@example.com
    - kind: Group                # every member of the group is this agent
      name: oidc:agents-fixer
  sponsor:                       # required: the accountable person
    user: alice@example.com
    team: payments               # optional, shown and exported, not used by policy
  scope:                         # where this agent may act
    namespaces: ["payments-*"]   # exact names, or a prefix ending in "*"; ["*"] = everywhere; empty = nowhere
    pipelines: ["*"]             # Pipeline names, same form; empty or absent = all
    environments: ["test", "uat"]# environments it may approve or override in, same form; empty or absent = all
  autonomy: approve              # auto | approve | deny; default approve (section 10.4)
  suspended: false               # true: every write by its subjects is denied
status:
  ready: true
  reason: ""
  observedGeneration: 3
  conditions: [...]              # Ready, with reasons Valid, SubjectConflict, InvalidSponsor
```

**Validation (CRD schema and CEL rules).**

- `subjects`: 1 to 16 entries. `kind` is `ServiceAccount` (requires `namespace` and `name`),
  `User` or `Group` (require `name`, forbid `namespace`). Names are at most 253 characters.
  A ServiceAccount subject is stored and matched as its username,
  `system:serviceaccount:<namespace>:<name>`.
- `sponsor.user` is required, at most 253 characters, and must not start with `system:`:
  a sponsor is a person, never a ServiceAccount, a node or a built-in identity.
- The sponsor may not be one of the agent's own User subjects.
- `scope` lists hold at most 64 entries each. `*` may only end an entry.
- The Group subjects `system:authenticated`, `system:unauthenticated`, `system:serviceaccounts`
  and `system:masters` are refused: they would make most of the cluster one agent.

**Reconciler.** `pkg/reconciler/agentidentity` validates what the schema cannot and writes
`status.ready`. It reads every AgentIdentity (one informer) and refuses:

- **SubjectConflict**: a subject that another AgentIdentity already lists. The older
  AgentIdentity (by `creationTimestamp`, then name) keeps the subject; the newer one is not
  ready. A principal is never two agents.
- **InvalidSponsor**: a sponsor that is a subject of any AgentIdentity: an agent cannot sponsor
  an agent.

It runs with one worker, so conflict decisions are made one at a time and are deterministic. It
is idempotent: the same AgentIdentities always produce the same status.

**Lifecycle.**

- *Suspend* (`spec.suspended: true`) is the kill switch for one agent. Admission denies every
  stamped write by its subjects. Writes already made are untouched.
- *Delete* removes the registration. A deleted agent's ServiceAccount subjects become `bot`
  and its User and Group subjects become `human` (section 4.2). So a deletion would quietly turn an
  agent into a person. The `<release>-agent-identities` policy therefore admits a DELETE only
  when the stored object has `spec.suspended: true`: suspend first, remove the agent's RBAC,
  then delete.
- A **not-ready** AgentIdentity's subjects are not agents, but they are not people either: the
  index (section 5.4) lists them as blocked, and admission denies their stamped writes with the
  AgentIdentity's reason. An admin's mistake fails closed.

**RBAC.** Only cluster-wide admins write AgentIdentities: cluster admins, and holders of
`<release>-admin` through a ClusterRoleBinding. A namespace RoleBinding cannot grant a
cluster-scoped kind, so no namespace role reaches them; the viewer role reads them. The chart grants `get, list, watch` on `agentidentities` and `agentindexes` to
`system:authenticated` (`actors.directory.public`, default `true`): an agent must read the
index to write its own stamp, and showing "shipped by agent fixer for alice" in the UI already
discloses the registration. With `false`, bind `<release>-agent-directory-reader` to the agents
and people who need it.

## 4. The actor

### 4.1 Shape

One Go type, `v1alpha1.Actor`, used everywhere an actor is recorded:

```yaml
actor:
  kind: agent                       # human | agent | bot | controller   (verified)
  principal: system:serviceaccount:ci:fixer  #                           (verified)
  agent: fixer                      # set only when kind is agent        (verified)
  sponsor: alice@example.com        # set only when kind is agent        (verified)
  via: ui                           # ui | api | subscription | auto-rollback; controller-written only (verified)
  asserted:                         # recorded as written, never trusted (asserted)
    model: claude-opus-5            # at most 128 characters
    session: 7f3c2a                 # at most 128 characters
    client: kardinal-mcp/0.12.0     # at most 128 characters
```

`kind`, `principal`, `agent`, `sponsor` and `via` are verified (section 5). Everything under
`asserted` is not. The split is structural, so no consumer can read an asserted field by
accident: CEL sees `actor.asserted.model`, never `actor.model`.

The **`kardinal.io/actor` stamp** is this value, written in the record's own spec:

| Record | Where the stamp lives | Immutable by |
|--------|-----------------------|--------------|
| Bundle | `spec.actor` | CRD transition rule: `spec.actor` cannot be added, changed or removed after create |
| Approval | `spec.actor` | the existing `self == oldSelf` rule on the Approval spec |
| Gate override | `spec.overrides[].actor` | the gate-overrides policy (an existing entry cannot change) |
| AuditEvent | `spec.actor` | the existing `self == oldSelf` rule on the AuditEvent spec |
| Verdict (E8) | `spec.actor` | E8 |

The stamp is a field, not an annotation, because ValidatingAdmissionPolicy CEL has no JSON
parser (section 5.1), override entries cannot carry annotations, and a typed field gets an enum and
length limits from the schema. The v0.11 names stay and must agree with the stamp:
for a human, agent or bot stamp, `actor.principal` equals `kardinal.io/created-by`, Approval
`spec.user` and override `createdBy` when both are set. A controller stamp's principal is the
controller's ServiceAccount, and `kardinal.io/created-by` keeps its v0.11 component name
(`subscription:<name>`, `kardinal-controller`).

### 4.2 Kinds

The kind follows from the principal and the index. It is never chosen by the client:

| Kind | When | principal | agent, sponsor |
|------|------|-----------|----------------|
| `agent` | The principal, or one of its groups, is a subject of a ready, unsuspended AgentIdentity | the principal | from the AgentIdentity |
| `controller` | kardinal acts on its own: an automatic rollback, a Subscription's Bundle | the controller's ServiceAccount | unset; `via` says which |
| `bot` | A ServiceAccount (`system:serviceaccount:` prefix) that is not an agent, or the Bundle API's static token (principal `bundle-api`) | the principal | unset |
| `human` | Everything else | the principal | unset |

When the controller writes on behalf of a caller it has authenticated (the UI API, and the
Bundle API with TokenReview), the actor is the caller's actor, with `via: ui` or `via: api`.
It is not the controller's.

`human` means "not registered as an agent and not a ServiceAccount". An OIDC user that is really
a script is `human` until it is registered. The user guide says so, and section 11 lists it.

### 4.3 Records without a stamp

v0.11 records have no stamp, and kubectl users and old CLIs may still write without one.
Consumers see them as:

```yaml
actor: { kind: unknown, principal: <kardinal.io/created-by, spec.user or createdBy, if any> }
```

`unknown` is a read-side value only: the schema does not accept it in a stamp. Section 5.5 says
who may write without a stamp: anyone but an agent.

## 5. Verifying the actor at admission

### 5.1 What a ValidatingAdmissionPolicy can and cannot do (from source)

| Fact | Source (k8s.io/apiserver v0.37.1) |
|------|-----------------------------------|
| The CEL variables are `object`, `oldObject`, `request`, `params`, `namespaceObject`, `authorizer`, `authorizer.requestResource` and `variables`. Nothing else: no client and no lookup. | `pkg/admission/plugin/cel/compile.go:34-41` |
| `params` is the whole param object, status included, typed `dyn`. | `compile.go:259` |
| A binding's `paramRef` names one object (`name`) or selects several (`selector`). It is static: no CEL picks the param per request. | `pkg/admission/plugin/policy/generic/policy_dispatcher.go:338-393` |
| With a selector, each matching param is a separate invocation, and any failure denies. So a selector gives "for all params", never "there exists a param". | `policy_dispatcher.go:191` |
| With zero matching params and `parameterNotFoundAction: Allow`, the policy is not evaluated at all. With `Deny` the request is refused. | `policy_dispatcher.go:189-191, 400` |
| A named param is read from an informer, with a direct GET if the informer does not have it yet. | `policy_dispatcher.go:345-354` |
| `request.userInfo` has `username`, `uid`, `groups` and `extra` (a map of string lists). | `pkg/admission/plugin/cel/condition.go:108-120` |
| Bound ServiceAccount tokens add `authentication.kubernetes.io/credential-id`, `pod-name`, `pod-uid` and `node-name` to `extra`. OIDC claims reach `extra` only through `claimMappings.extra` in structured authentication configuration. | `pkg/authentication/serviceaccount/util.go:38-48`; `pkg/apis/apiserver/types.go:274` |
| The CEL libraries are fixed per Kubernetes version. There is no JSON parsing. `ext.Strings`, `ext.Sets` and `authorizer` are there from 1.27 to 1.29, two-variable comprehensions from 1.32. | `pkg/cel/environment/base.go:55-195` |
| `managedFields[].manager` is the client's `fieldManager`, or the prefix of its User-Agent. The API server records no user in managedFields. | `pkg/endpoints/handlers/create.go:259-280`; `apimachinery/pkg/apis/meta/v1/types.go:1409-1444` |
| MutatingAdmissionPolicy is alpha in 1.32, beta and off in 1.34, and on by default only from 1.36. | `pkg/features/kube_features.go:420-424` |

The chart supports Kubernetes 1.35 and later. Section 5.3 uses only what 1.35 has.

### 5.2 Options

The question: a client claims "I am agent `fixer`, sponsored by `alice`". How does admission check
that claim against AgentIdentity, when a policy cannot fetch the AgentIdentity named in the
request?

**(a) The controller mirrors the registry into one params object.** A cluster-scoped
`AgentIndex` per release, `status.users` and `status.groups` maps from a subject to its agent
entry (agent, sponsor, scope, autonomy, blocked reason). One binding with `paramRef.name` reads
it. Admission recomputes the kind, agent and sponsor from `request.userInfo` and the index, and
compares them with the stamp.
- Pro: everything is decided at write time, in one place. That covers kind, agent, sponsor,
  scope, suspension, conflicts and group subjects (the request's `groups` are right there). A
  forged or missing stamp is denied with a message that names the stamp to write. Every record
  carries its stamp in its own spec, frozen when written. External readers need nothing but the
  object.
- Con: a derived copy. A registry change takes effect when the index is rewritten (one
  reconcile, sub-second normally; stale while the controller is down). It is one object, so
  about 5,000 subjects at most, and kardinal supports 4,000 (section 5.4). Clients must read the index to write the stamp.

**(b) The controller stamps the actor after admission, from managedFields.** Not possible.
managedFields record a client-chosen manager name, not the authenticated user (5.1). After
admission nothing on the object says who wrote it, so the controller would have to trust the
client.

**(c) Admission checks only the principal; the controller derives agent and sponsor into
status.** The v0.11 policies already pin the principal. The Bundle reconciler would look the
principal up in AgentIdentity and write `status.actor`, and gates would read status. Approvals
and overrides have no status of their own, so the PolicyGate reconciler would derive them at
tally time.
- Pro: no index object; clients write nothing new; one source of truth.
- Con: **group subjects cannot work.** The Bundle records the username, not the requester's
  groups, so after admission nobody can tell that the creator was in `oidc:agents-fixer`.
  Recording all groups would put the requester's full group list on every object. Scope
  violations are admitted and then held, not denied. An Approval's actor would be recomputed from
  the registry as it is at tally time, not at approval time. Three kinds of record would carry
  their actor in three different places.

**(c') Variant: (c) plus a selector binding over AgentIdentities.** Admission evaluates each
AgentIdentity as a param ("if this identity matches the requester, the stamp must name it").
- Con: a selector gives "for all", not "exists" (5.1). A principal that matches no identity can
  claim to be any agent that does not exist. If that agent is registered later, the claim
  becomes valid after the fact. The controller cannot close this hole, because the claim was
  never checked. A human could name a fake agent and a fake sponsor, and then pass
  `excludeAuthor` on their own work.

**(d) RBAC as the index (`authorizer`).** Admins grant each agent principal a custom verb,
`act-as` on `agentidentities/<name>`. The policy checks
`authorizer.group('kardinal.io').resource('agentidentities').name(stamp.agent).check('act-as').allowed()`.
- Pro: no copy; RBAC is the registry; group subjects work.
- Con: "is this principal any agent?" needs a second, name-less permission, so a missing grant
  silently makes an agent a human. The sponsor and scope cannot be checked (RBAC has no data).
  The registry is split between AgentIdentity and RBAC that must agree. Either the controller
  writes ClusterRoleBindings, or admins write each grant twice.

**(e) A MutatingAdmissionPolicy writes the stamp.** The policy writes `spec.actor` itself from
`request.userInfo` and the index (option a's params). The stamp is verified by construction, and
clients write nothing.
- Con: on by default only from Kubernetes 1.36 (5.1). The chart supports 1.35.

### 5.3 Decision: (a), the index, with the controller exempt

**Option (a).** It is the only option that verifies all four verified fields at write time on
Kubernetes 1.35, supports group subjects, and denies out-of-scope and suspended agents instead
of holding them. Its cost, a derived index with propagation lag, is the same lag (c) has: (c)
reads the same registry through the controller's cache. The lag is bounded and measured (the
live e2e asserts that a suspension takes effect within 10 s).

**(e) is the upgrade path.** When the minimum supported Kubernetes reaches 1.36, a
MutatingAdmissionPolicy with the same params can write the stamp. Clients then stop computing
it, and the validating policy below stays as defense in depth. Tracked as a follow-up.

**(c') is rejected** because of the fabricated-agent hole, and (b) because it is impossible.

The policy, `<release>-actor`, matches CREATE of `bundles` and `approvals`;
`<release>-actor-overrides`, with the same expressions, matches CREATE and UPDATE of
`policygates` labelled `kardinal.io/override-check` (new override entries; a params-free
`<release>-override-marker` requires the label on every write that adds one, #1936). Their
bindings have `paramRef: {name: <release>, parameterNotFoundAction: Deny}`. The core of it:

```yaml
variables:
  - name: controller            # this release's controller and admission.controllerUsernames
    expression: request.userInfo.username in [...]
  - name: idx
    expression: 'has(params.status) ? params.status : {}'
  # Every index entry that matches the requester: by username, then by group.
  - name: matches
    expression: >-
      ((has(variables.idx.users) && request.userInfo.username in variables.idx.users)
        ? [variables.idx.users[request.userInfo.username]] : []) +
      (has(variables.idx.groups)
        ? request.userInfo.groups.filter(g, g in variables.idx.groups).map(g, variables.idx.groups[g]) : [])
  - name: kind
    expression: >-
      variables.matches.size() > 0 ? 'agent' :
      request.userInfo.username.startsWith('system:serviceaccount:') ? 'bot' : 'human'
  - name: stamps                # the stamps this request adds (one, or the new override entries)
    expression: ...
validations:
  - expression: variables.controller || variables.matches.all(m, m.agent == variables.matches[0].agent)
    message: you match more than one AgentIdentity; ask an admin to fix the registry
  - expression: variables.controller || variables.matches.all(m, !has(m.blocked))
    messageExpression: "'agent ' + variables.matches[0].agent + ' may not act: ' + variables.matches[0].blocked"
  # An agent must stamp; anyone else may leave the stamp out (section 4.3).
  - expression: variables.controller || variables.kind != 'agent' || variables.stamps.all(s, s != null)
    messageExpression: "'you are agent ' + ... + '; set actor {kind: agent, principal: ..., agent: ..., sponsor: ...}'"
  # A stamp says exactly what the index says.
  - expression: >-
      variables.controller || variables.stamps.all(s, s == null || (
        s.principal == request.userInfo.username && s.kind == variables.kind && !has(s.via) &&
        (variables.kind == 'agent'
          ? s.agent == variables.matches[0].agent && s.sponsor == variables.matches[0].sponsor
          : !has(s.agent) && !has(s.sponsor))))
    messageExpression: ...      # names the expected stamp
  # Scope: namespace always; the Bundle's spec.pipeline; the Approval's spec.environment;
  # the gate instance's kardinal.io/pipeline and kardinal.io/environment labels.
  - expression: variables.controller || variables.kind != 'agent' || <scope check of matches[0]>
```

The chart's tests render this policy and evaluate it against each case in section 13 on a real
API server (kind). Unit tests evaluate the Go derivation (5.6) against the same cases, so the two
cannot drift apart.

**The controller is exempt, and stamps for its callers.** For the UI API and the Bundle API with
TokenReview, the controller has the caller's full `UserInfo` (username and groups) from the
TokenReview. It derives the actor with the same Go function the index is built with, reading its
own AgentIndex. It refuses the request itself when admission would refuse it (403 with the same
message), then writes with `via`. For its own Bundles it writes `kind: controller`.

### 5.4 The AgentIndex

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentIndex               # cluster-scoped, one per release, named after the release
metadata:
  name: kardinal-promoter
status:
  observedIdentities: 12       # AgentIdentities read
  users:                       # key: a username (ServiceAccount subjects as system:serviceaccount:ns:name)
    "system:serviceaccount:ci:fixer":
      agent: fixer
      sponsor: alice@example.com
      namespaces: ["payments-*"]
      pipelines: ["*"]
      environments: ["test", "uat"]
      autonomy: approve
  groups:
    "oidc:agents-fixer": { agent: fixer, ... }
  # A not-ready or suspended AgentIdentity's subjects carry `blocked: <reason>`.
  conditions: [...]            # Ready; IndexFull when the next entry would pass 1 MiB
```

- **Builder.** `pkg/reconciler/agentindex` watches AgentIdentities and writes only its own
  AgentIndex's status: a status-writing reconciler. It creates the AgentIndex if it is missing,
  at startup and whenever it is deleted. The same AgentIdentities always produce the same status
  (keys sorted, no timestamps in entries), so a re-run writes nothing. The same build writes each
  AgentIdentity's `status.ready`, patching only those that change: one evaluation per change,
  not one per AgentIdentity (#1937).
- **Why not a kro Graph.** A Graph with a selector `ref` over AgentIdentities and one `template`
  node could build this object. kro can do it: a namespaced Graph may list a cluster-scoped kind
  (`executor/simple.go:1259-1265`) and apply a cluster-scoped object (`compiler/context.go:279-285`),
  and the kro CEL environment has `transformMapEntry` (`pkg/cel/environment.go:130`). We do not
  use one, for two reasons:
  1. `transformMapEntry` fails the whole expression on a duplicate key. One conflicting or
     invalid AgentIdentity would freeze the index for everyone, where we need first-wins with the
     loser blocked.
  2. Admission freshness would depend on kro's single Graph queue, which every promotion Graph
     shares (G13). A suspension would then wait behind promotion backlog.
  The row is added to ledger G8.
- **Size.** etcd stores at most 1.5 MiB per object. An entry is about 210 bytes (one subject, a
  namespace scope and a sponsor, as measured at 5,000 AgentIdentities), so the builder stops at
  1 MiB (`actor.MaxIndexBytes`), about 5,000 such subjects. kardinal supports up to 4,000
  AgentIdentities: about 80% of the bound, where admission stays under 50 ms
  ([Scale and limits](../operate.md#scale-and-limits)). Past the bound the index is `IndexFull`: new AgentIdentities
  get `status.ready: false` with reason `IndexFull`, and their subjects are blocked. Existing
  entries keep working. The scale suite registers 2,000 AgentIdentities.
- **Writers.** Only the controller has `update` on `agentindexes/status`. Nothing else writes the
  index (the chart's user roles do not include it).
- **Order.** One build writes the index first, then the AgentIdentity statuses that changed,
  then a `StatusesWritten` condition on the index. Admission reads only the index, so a
  suspension takes effect when it is written, not after a mass status change at the client's
  rate limit (2,000 patches at 20 QPS are 100 s). The `agent.*` gates read the index and
  `status.ready`, and wake on index events: the condition write wakes them once the statuses
  are written. Until then a new agent is not yet registered for them (fail closed).
- **Missing index.** `parameterNotFoundAction: Deny`: with the param missing, the dispatcher
  refuses every write the policy matches before any expression runs, the controller's and
  kro's included (`CollectParams`, `policy_dispatcher.go:396-398`, runs at
  `validating/dispatcher.go:151`, before `Validate` at `:215`; matchConditions run inside
  `Validate`, `validator.go:103-121`, so they cannot exempt anyone). In-flight gated promotions
  would stall (#1929). So the index is never missing: the chart creates it (Helm installs the
  kinds it does not list alphabetically, `AgentIndex` before the policies; `helm.sh/resource-policy:
  keep`), and `<release>-agent-index` lets only the controller delete it or write its status. An
  index not built yet is not Ready: the `check` variable then exempts kro and the controller, and
  only checked writes wait. `Allow` was rejected: with no params the policy is not evaluated at
  all (fail open). A missing CRD still fails closed, and the upgrade guide applies CRDs before
  the chart.
- **Cost.** The API server converts the whole param object, `managedFields` included, to a CEL
  value for every request a policy with params matches (`cel/activation.go:76-77`, a JSON round
  trip). So the policies match only what they check before the param is read (matchConstraints
  and the object selector run before `CollectParams`), and the builder clears the index's
  `managedFields` after each status write (#1936).

### 5.5 Who may write without a stamp

- An agent: never. The policy denies it and the message names the stamp to write.
- A human or a bot: yes, as in v0.11. Old CLIs, kubectl and GitOps tools keep working. The
  record reads as `kind: unknown` (4.3), and E7 policies can require a stamp.
- The controller: always stamps (5.3).

### 5.6 One derivation, three callers

`pkg/actor` holds `Derive(userInfo, index) (Actor, error)` and `Scope.Allows(namespace, pipeline,
environment)`. The index builder, the controller's on-behalf writes and the CLI all call it. The
VAP CEL is its transliteration. A table-driven test runs every case in section 13 through
`Derive`. The live admission tests run the same cases through the API server.

## 6. Writing the stamp

| Writer | How it gets the stamp |
|--------|-----------------------|
| `kardinal create bundle`, `promote`, `rollback`, `approve`, `override` | `SelfSubjectReview` (username and groups), then a list of AgentIndexes; `actor.Derive`. Asserted fields come from the environment: `KARDINAL_ACTOR_MODEL`, `KARDINAL_ACTOR_SESSION` and `KARDINAL_ACTOR_CLIENT`; unset ones record nothing. An agent out of its scope, suspended or ambiguous is refused before anything is written. If the index cannot be read, the CLI writes no stamp. That is accepted for people and bots (5.5); an agent is then denied with the stamp it must write. |
| UI API | The controller, from the TokenReview `UserInfo`, with `via: ui` (5.3). |
| Bundle API, TokenReview token | The same, with `via: api`. |
| Bundle API, static token | `kind: bot`, `principal: bundle-api`, `via: api`. |
| Subscription | `kind: controller`, `via: subscription`; the Subscription's name is in `kardinal.io/created-by` (`subscription:<name>`). |
| Automatic rollback | `kind: controller`, `via: auto-rollback`. |
| kubectl / GitOps | Optional. A person may write it by hand. An agent must. |

The CLI's AgentIndex list is cluster-wide. Every release builds its index from the same
AgentIdentities, so any ready index gives the same answer. The CLI uses the first ready one by
name. Each release's policy checks against its own index, so during a few seconds of lag the CLI
and the policy can disagree. The write is then denied with the expected stamp, and a retry
succeeds.

## 7. AuditEvent.actor and API AuditEvents (#1539)

**Schema.** `AuditEventSpec` gains:

- `actor` (optional `Actor`): who caused the event. Absent on v0.11 records.
- `request` (optional): `{route, method}` for API writes, for example
  `{route: /api/v1/ui/pipelines/{name}/pause, method: POST}`. No body and no token.

`bundleName`, `pipelineName` and `environment` become optional. A pause has no Bundle, and an
API write that fails before it resolves the Pipeline has no Pipeline. A CRD rule requires at
least one of them. Existing records validate unchanged.

**Who sets `actor`.**

| Event | actor |
|-------|-------|
| `ApprovalRecorded`, `ApprovalRevoked` | the Approval's stamp |
| `GateOverridden` | the override entry's stamp |
| `BundleRejected` | the rejection's principal, kind `unknown` unless the UI wrote it (`via: ui`) |
| `PromotionStarted` and the other promotion events | the Bundle's stamp: the change belongs to its author |
| API writes (new) | the caller, as the controller authenticated them |

**API writes.** Every successful write through the UI API or the Bundle API writes one
AuditEvent with action `APIWrite`. The outcome is `Success`, or `Failure` when the controller
refused or the API server denied the write. The message names the route's verb ("paused
pipeline checkout", "created bundle checkout-7f3c2a"). One action and a `request.route` are
easier to query and export than one action per route, and the route says which. The writes are
queued and written after the response, at most 50 a second per controller replica: past that,
or with the queue (256) full, the AuditEvent is dropped and counted
(`kardinal_audit_events_dropped_total{kind="APIWrite"}`, reasons `rate`, `queue`, `error`). The
access log line is still written (#1537). They are retained under
`--audit-retention-max-age` (#1523).

**Default: on** (`controller.accessLog.auditEvents: true`). #1539 proposed off because of volume.
These writes come at human speed and are rate-limited, and "a verified actor on every change"
means nothing if the record is opt-in. Off remains available.

**Only the controller names an actor in an AuditEvent.** A new `<release>-audit-actor` policy
admits an AuditEvent CREATE with `spec.actor` only from this release's controller (and
`admission.controllerUsernames`). Records without an actor stay as before. The user roles grant
no write on AuditEvents. So an `actor` in the audit log was written by kardinal.

**`kardinal audit export`.**

```
kardinal audit export [--pipeline P] [--since 24h] [-A] [--format json|ocsf] [--file F]
```

- Writes NDJSON, one event per line, page by page from a paginated List (500 per page), so a
  large log is streamed, not held in memory. The order is the API server's, not time: sort
  downstream.
- `json`: the AuditEvent spec, plus `namespace`, `name` and `uid`.
- `ocsf`: OCSF 1.3.0 **API Activity** (`class_uid: 6003`, `category_uid: 6`). It maps
  `activity_id` (Create 1, Update 3, Delete 4, Other 99) and `type_uid = 600300 + activity_id`;
  `time` (epoch ms) and `status_id` (Success 1, Failure 2, Pending 99); `actor.user.name`
  (principal), `actor.user.type` (`User` for human, `System` for bot and controller, `Agent`
  with `type_id` 99 for an agent) and `actor.app_name` (agent); `src_endpoint.svc_name`;
  `api.operation` (action) and `api.request.uid` (AuditEvent uid); `resources[]` (Bundle,
  Pipeline, environment); `metadata.product` (`kardinal-promoter`, version) and `message`.
  Fields OCSF has no place for, such as `sponsor`, `kind`, `via` and `asserted`, go under
  `unmapped.kardinal`. The field names were checked against the OCSF 1.3.0 schema
  (`events/application/api_activity.json` and the objects it uses).

## 8. Showing the actor

One sentence form everywhere, from `actor.Describe`:

| Actor | Sentence |
|-------|----------|
| agent | `agent fixer for alice@example.com` |
| human | `alice@example.com` |
| bot | `bot system:serviceaccount:ci:builder` |
| controller | "kardinal (subscription)", "kardinal (auto-rollback)" |
| unknown with principal | `alice@example.com` |
| unknown without | `unknown` |

`via: ui` and `via: api` append " via the UI" or " via the API". Asserted fields are shown
apart, labelled as stated by the client: `model claude-opus-5 (asserted)`.

- **PR evidence**: the `Created by:` row becomes the sentence above, plus an `Agent` row with
  the sponsor and the asserted model and session when the kind is agent.
- **UI**: the stage drawer and the history list show "shipped by agent fixer for alice". The
  approvals list shows each approver's sentence.
- **CLI**: `kardinal history` gains an `ACTOR` column. `kardinal why` names the actor in the
  author sentence and in each approval. `-o json` carries the `actor` object.

## 9. Upgrade from v0.11.0

- Stored Bundles, Approvals, PolicyGates and AuditEvents have no `actor`. Every new field is
  optional. The transition rule on `spec.actor` only forbids adding one later, so existing
  objects update as before (status, rejection).
- Consumers read them as `kind: unknown` with the v0.11 principal (4.3). `excludeAuthor`
  keeps working from `kardinal.io/created-by`.
- No AgentIdentities exist after an upgrade, so every principal is `human` or `bot`, and stamps
  are optional for them. Nothing is denied that v0.11 admitted, apart from the cases this spec
  adds (forged stamps, agents).
- The CRDs (`agentidentities`, `agentindexes`, and the changed `bundles`, `approvals`,
  `policygates` and `auditevents`) are applied before the chart, as every upgrade already
  requires. The live upgrade test installs v0.11.0, creates a Bundle and an Approval,
  upgrades, and checks both promote and show `unknown`.

## 10. How later epics consume the actor

### 10.1 CEL variables (PolicyGate expressions)

Built by the PolicyGate reconciler next to `bundle.*`:

| Variable | Type | Source |
|----------|------|--------|
| `actor.kind` | string | the Bundle's stamp; `unknown` without one |
| `actor.principal` | string | the stamp; else `kardinal.io/created-by`; else `""` |
| `actor.agent`, `actor.sponsor` | string | the stamp; `""` unless the kind is agent |
| `actor.via` | string | the stamp; `""` |
| `actor.verified` | bool | true when the Bundle has a stamp |
| `actor.asserted.model`, `.session`, `.client` | string | the stamp; `""` |
| `agent.registered` | bool | true when the Bundle's agent has a ready AgentIdentity |
| `agent.name`, `agent.sponsor` | string | the AgentIdentity, live |
| `agent.autonomy` | string | `auto`, `approve` or `deny`, live |
| `agent.suspended` | bool | live |

`actor.*` is fixed at the Bundle's creation. `agent.*` is the AgentIdentity now, so an admin who
suspends an agent, or tightens its autonomy, changes in-flight promotions.

`bundle.createdBy` stays as an alias of `actor.principal` for one release.

### 10.2 Where `agent.*` comes from: a `ref` node

> **Amended** by [18-actor-policy.md §2.2](18-actor-policy.md#22-where-agent-comes-from): the
> PolicyGate reconciler reads the AgentIdentity, so no ref node and no Graph RBAC (G18, G19) are
> needed. The analysis below is kept for the Graph-native path once kro fixes G18 and G19.

The translator adds a `ref` node named `agentIdentity`, but only to Graphs whose Bundle has an
agent stamp, so there is no `includeWhen`:

```yaml
- id: agentIdentity
  ref:
    apiVersion: kardinal.io/v1alpha1
    kind: AgentIdentity
    metadata:
      name: fixer              # from the Bundle's spec.actor.agent, at translation time
```

Gate templates copy what they need into the instance (`spec.agent: {name, sponsor, autonomy,
suspended, ready}` from `${agentIdentity.spec...}` and `${agentIdentity.status.ready}`). The
PolicyGate reconciler reads `agent.*` from its own spec, so the data reaches the gate through
CRD fields. A Graph for a Bundle without an agent gets the constant `agent.registered: false`.

Checked against kro v0.10.0-rc.0:

- A namespaced Graph may `ref` a cluster-scoped object. `defaultNamespace` leaves cluster-scoped
  kinds alone (`executor/simple.go:1576-1583`), and the GET uses an empty namespace
  (`simple.go:1208-1216`). The compiler requires that no namespace is set
  (`compiler/context.go:279-285`).
- A missing target is not an error. The node is not ready (`simple.go:1208-1214`), the Graph's
  `ResourcesConverged` is False, kro retries with backoff, and the dependents wait. A Bundle whose agent was deleted therefore holds, which fails closed.
  `kardinal why` names the reason from the Graph's condition.
- The read is made as the Graph's impersonated ServiceAccount, so that identity needs `get` on
  `agentidentities`. That is a cluster-scoped grant for a per-namespace identity:
  **[G19](16-graph-capability-ledger.md#g19-a-namespaced-graph-identity-needs-a-cluster-wide-grant-to-read-a-cluster-scoped-ref)**.
- kro re-reads the ref on a change only if the identity may **watch the whole kind**. The
  watch check drops the ref's name (`impersonation.go:203-221`), and a denied check silently
  degrades drift detection (`simple.go:1436-1456`). kro's docs say "kro watches every ref
  target". Without the watch, an `autonomy` change would reach the gate only on the next
  unrelated Graph reconcile: **[G18](16-graph-capability-ledger.md#g18-the-ref-watch-check-ignores-the-refs-name)**.

The workaround for both: the IdentityProvisioner keeps one chart-created ClusterRoleBinding,
`<release>-graph-agent-reader` (`get, list, watch` on `agentidentities`), whose subjects are the
Graph ServiceAccounts of the namespaces that have an agent Bundle. The controller may only
`patch` that one binding (`resourceNames`), and it holds the role's permissions itself, so RBAC
escalation checks pass without `bind` or `escalate`. The ledger has the upstream asks.

### 10.3 The one-party rule

An agent and its sponsor count as one party. Formally:

```
party(a) = {a.principal}                       if a.kind != agent
party(a) = {a.principal, a.sponsor, agent:<a.agent>}   if a.kind == agent
same(a, b) = party(a) ∩ party(b) ≠ ∅
key(a)   = a.kind == agent ? a.sponsor : a.principal   # one vote per key
```

`actor.SameParty(a, b)` and `actor.PartyKey(a)` in Go. E7 and E8 use them wherever kardinal
compares identities:

- **excludeAuthor**: an approval does not count when `same(author, approver)`. Alice cannot
  approve her agent's Bundle, her agent cannot approve hers, and two agents with the same
  sponsor cannot approve each other's.
- **Counting**: `required: N` counts distinct `key`s. Two agents of one sponsor, or an agent
  and its sponsor, are one approval.
- **Agent approvals (opt-in, E7)**: an agent's approval counts only when the gate says
  `allowAgents: true` and lists the agent in `allowedAgents`. It never counts on work of its own
  party, whether or not `excludeAuthor` is set.
- **Verdicts (E8)**: a verdict counts only from a verifier with `!same(author, verifier)`.

For an author with `kind: unknown`, `party` is `{principal}` when it is known. Without a
principal, `excludeAuthor` holds the gate, as in v0.11.

### 10.4 Autonomy

`AgentIdentity.spec.autonomy` is the agent's ceiling. E7's per-environment `agents:` shorthand
on the Pipeline is the environment's ceiling. The effective value is the stricter of the two
(`deny` > `approve` > `auto`). Neither the agent's admin nor the Pipeline owner can loosen the
other's limit. E7 compiles it into ordinary gate nodes.

### 10.5 Graph templates

`${bundle.spec.actor.kind}` and the other stamp fields are available to translator-generated
Graph expressions through the Bundle `ref` node every Graph already has. `schedule.*`-style
context is not needed: the stamp is data on the Bundle.

## 11. What this does not protect against

- **A stolen agent credential is the agent.** The actor is as good as the API server's
  authentication. Suspend the AgentIdentity and revoke the credential.
- **An unregistered agent is a human or a bot.** Registration is opt-in for the organisation:
  kardinal cannot detect an AI behind an OIDC user that nobody registered. Policies that must
  hold agents back can require `actor.verified` and `actor.kind == 'human'` from principals in a
  trusted group.
- **Asserted fields are claims.** A model name is whatever the client wrote. v0.13 may require
  them signed, or verify them through `claimMappings.extra` when the agent's OIDC token carries
  them (5.1).
- **Index lag.** A registry change takes effect at admission after one index reconcile. While
  the controller is down, the index is stale, but it is not removed.
- **Cluster admins.** They can write AgentIdentities, the index and the policies. The registry
  is theirs by design.
- **`edit`/`admin` holders in the controller's namespace** can impersonate the controller and
  so pass as it (the same trust boundary as G16 for the Graph identity).

## 12. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| A1 | How admission checks a claimed actor | (a) index params, (b) managedFields, (c) principal only + derive into status, (c') + selector over AgentIdentities, (d) RBAC `authorizer`, (e) MutatingAdmissionPolicy | **(a)**, 5.3: the only option that verifies kind, agent, sponsor and scope at write time on every supported minor, with group subjects. (e) later. |
| A2 | Who builds the index | Go reconciler on its own status; kro Graph; chart-rendered static list | **Go reconciler**, 5.4: kro CEL fails the whole index on one conflict, and kro's single queue would delay revocation (G13). Ledger G8 row. |
| A3 | Where the stamp lives | annotations; one JSON annotation; typed `actor` field | **Typed field**: the policy CEL cannot parse JSON (5.1), override entries cannot carry annotations, and the schema enforces enum and lengths. |
| A4 | Sponsor: user or group | user; group; either | **User, with an optional informational team.** The one-party rule needs a decidable comparison. An approver's username is verified, but their full group list is not recorded on the Approval. |
| A5 | Agents writing without a stamp | allow and derive; deny; allow only for humans and bots | **Deny agents, allow others**: an agent cannot slip past agent policy by omitting the stamp, and v0.11 clients keep working. |
| A6 | Deleting an AgentIdentity | allow; finalizer with tombstone; require suspend first | **Require suspend first**: deletion turns an agent into a person; making it two steps makes that deliberate, with no finalizer to get stuck. |
| A7 | Source of `agent.*` in gates | `ref` node into gate spec; PolicyGate reconciler reads AgentIdentity; snapshot in the stamp | **`ref` node** (10.2): live, on the Graph, data through CRD fields; the RBAC cost is the G18/G19 workaround. The stamp keeps the at-creation sponsor for the one-party rule. |
| A8 | API write AuditEvents | one action per route; one `APIWrite` action with `request.route`; none | **`APIWrite` + route**: one enum value, queryable, and the route is exact. |
| A9 | Default for API write AuditEvents | off (#1539); on | **On**: human-paced, rate-limited, and the epic is "a verified actor on every change". |
| A10 | Directory readability | public to authenticated; agents only; admins only | **Public by default, switchable**: the CLI must read it to stamp, and the UI shows registrations anyway. |

## 13. Test plan

Admission cases (each runs through `actor.Derive` in unit tests and through the chart's policy on
kind):

| # | Requester | Stamp | Expect |
|---|-----------|-------|--------|
| 1 | human `alice` | none | admitted; reads `unknown` |
| 2 | human `alice` | `{human, alice}` | admitted |
| 3 | human `alice` | `{human, bob}` | denied: principal |
| 4 | human `alice` | `{agent, alice, fixer, alice}` | denied: not an agent |
| 5 | agent SA `ci:fixer` | none | denied: agent must stamp; message names the stamp |
| 6 | agent SA `ci:fixer` | `{agent, …, fixer, alice}` | admitted |
| 7 | agent SA `ci:fixer` | `{agent, …, fixer, mallory}` | denied: sponsor |
| 8 | agent SA `ci:fixer` | `{human, …}` | denied: kind |
| 9 | group agent (member of `oidc:agents-fixer`) | correct | admitted |
| 10 | agent, namespace out of scope | correct | denied: scope |
| 11 | agent, Bundle for a Pipeline out of scope | correct | denied: scope |
| 12 | agent, Approval for an environment out of scope | correct | denied: scope |
| 13 | suspended agent | correct | denied: suspended |
| 14 | principal in two AgentIdentities | — | second identity not ready; its subjects blocked |
| 15 | unregistered SA `ci:builder` | `{bot, …}` | admitted |
| 16 | controller on behalf of `alice` | `{human, alice, via: ui}` | admitted (exempt) |
| 17 | anyone but the controller | `via` set | denied |
| 18 | override entry by agent | correct / forged | admitted / denied |
| 19 | AuditEvent with `spec.actor` created by a non-controller | — | denied |
| 20 | AgentIdentity delete without suspend | — | denied |

Also:

- **Unit**: `Derive`, `Scope`, `SameParty`, `PartyKey`, `Describe`; index builder (conflicts,
  blocked, size limit, idempotency); AgentIdentity reconciler (validation, conflicts,
  idempotency); OCSF and JSON export (golden files); CLI stamping with a fake index.
- **Fake-client journeys**: an agent Bundle promoted through test, with history and `why`
  showing the actor; UI API and Bundle API writes producing `APIWrite` AuditEvents with the
  caller's actor.
- **Live e2e** on kind, COUNT=2, with `coverage.tsv` rows: the admission cases above, the
  suspension latency (under 10 s), agent Bundle end to end with PR evidence, the UI API
  AuditEvent, `audit export` in both formats, and the upgrade from v0.11.0.
- **Scale**: 2,000 AgentIdentities: index size under 1 MiB, Bundle admission latency within
  5 ms of the no-agent baseline.

## 14. Out of scope here (follow-ups)

- A MutatingAdmissionPolicy that writes the stamp (option e), once Kubernetes 1.36 is the minimum.
- Stamps on kubeconfig writes of pause, holds and rejections. Through the UI API they are
  already covered by `APIWrite` AuditEvents with the actor.
- Signed asserted fields (v0.13 receipts).
- E7's policy features, E8's Verdicts and E10's MCP tools, which consume this spec.
