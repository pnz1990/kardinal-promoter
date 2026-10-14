# Design: Verdicts (agents as verification gates)

> Status: Accepted (spec for epic #1844, v0.12.0). Implementation follows in separate PRs.
> Created: 2026-10-14
> Depends on: [17-actors.md](17-actors.md) (the actor stamp, `actor.SameParty`, the AgentIndex; #1834)
> and E7's one-party rule (#1843).
> Verified against: kro v0.10.0-rc.0 (`54a203b`) and Kubernetes `k8s.io/apiserver` v0.37.1.
> Source references are `path:line` in those trees, or in this repository.

---

## 1. Problem

An agent that writes code can also test it, and some of those tests are not scripts: an agent
explores the running environment the way a QA engineer would, and decides. The owner wants
those agents to act as **gates**: a version reaches prod only after N agents (or people, or CI)
say the version passed in uat. Three things make that hard:

- **Who said so.** A verdict is only worth something if the verifier is known and is not the
  author. An agent must not verify its own work, nor its sponsor's (the one-party rule of
  17-actors §10.3).
- **Agents are not deterministic.** One run passes, the next is inconclusive. The policy must
  say what happens then, in the open: how many runs, how many passes, what an inconclusive
  result does, how long to wait.
- **Agents in the cluster need bounds.** An exploratory agent runs code it chose. It must reach
  the environment under test and its model endpoint, and nothing else; read the target
  namespace, and write nothing but its verdict; have no git credentials; and stop.

This spec defines:

- `Verdict`, the record of one pass, fail or inconclusive result (section 3), and its identity
  admission (section 4);
- how the promotion Graph reads Verdicts into gates (section 5);
- the **verify gate**, `spec.verify` on a PolicyGate (section 6);
- `kardinal verify` (section 7);
- `AgentRun`, the in-cluster verifier: a Graph-owned run of an admin-registered agent image that
  writes one Verdict (section 8);
- retries and flakiness (section 9), and evidence in the PR, the UI and the audit log (section 10).

## 2. Terms

| Term | Meaning |
|------|---------|
| **Check** | A named verification, for example `exploratory-qa`. A DNS label. One verify gate waits for one check. |
| **Verified environment** | The environment the verifiers test: the version is already running there. A verify gate on `prod` usually verifies `uat`. |
| **Verdict** | One verifier's result for one (version UID, verified environment, check): `pass`, `fail` or `inconclusive`. |
| **Verifier** | Whoever wrote a Verdict: its actor (17-actors §4). |
| **Vote** | What a gate counts. One per voter (section 6.3). |
| **AgentRun** | One run of an in-cluster agent for one (version, environment, check, run index, attempt). It runs one Job, whose Pod writes one Verdict. |
| **StepTemplate** | The cluster-scoped registration of an agent image that AgentRuns run (section 8.2). |

## 3. The Verdict CRD

Namespaced (`verdicts.kardinal.io`, short name `vd`, categories `kardinal-all`), in the
Bundle's namespace, owned by the Bundle.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Verdict
metadata:
  name: checkout-1-5-0-uat-exploratory-qa-7f3c2a   # anything; for an AgentRun, the AgentRun's name (4.2)
  namespace: payments
  labels:
    kardinal.io/bundle: checkout-1-5-0             # = spec.bundle (admission)
    kardinal.io/environment: uat                   # = spec.environment (admission)
    kardinal.io/check: exploratory-qa              # = spec.check (admission)
  ownerReferences:                                 # optional; only the Bundle it is about (admission)
    - {apiVersion: kardinal.io/v1alpha1, kind: Bundle, name: checkout-1-5-0, uid: 1b2c...}
spec:
  pipeline: checkout                 # the Bundle's Pipeline
  bundle: checkout-1-5-0
  bundleUID: 1b2c...                 # the Graph counts it only for the Bundle with this UID
  environment: uat                   # the verified environment
  check: exploratory-qa
  result: pass                       # pass | fail | inconclusive
  summary: Checkout, refunds and the coupon flow work; 41 pages visited.   # <= 1024
  evidenceURL: https://ci.example.com/runs/88123   # optional, <= 2048, https:// or http://
  findings:                          # optional, at most 50
    - severity: low                  # info | low | medium | high | critical
      title: Coupon field accepts 300 characters    # <= 256
      detail: ...                    # <= 2048
      url: https://...               # optional, <= 2048
  by: system:serviceaccount:payments:qa-explorer   # the writer's username (admission)
  groups: []                         # writer's groups that count for allowedGroups (admission)
  actor:                             # the actor stamp (17-actors §4.1); required for agents
    kind: agent
    principal: system:serviceaccount:payments:qa-explorer
    agent: qa-explorer
    sponsor: bob@example.com
    asserted: {model: claude-opus-5, session: 7f3c2a, client: qa-explorer/1.4}
  run:                               # set only by an AgentRun's Pod (4.2)
    name: checkout-1-5-0-uat-exploratory-qa-r2-a1  # the AgentRun
    index: 2
    attempt: 1
  startedAt: "2026-10-14T10:02:11Z"  # asserted: when the verifier started
  durationSeconds: 412               # asserted
```

- **Immutable.** `self == oldSelf` on the spec, as on Approvals. A Verdict is revoked by deleting
  it (section 4), never edited.
- **No status.** Nothing about a Verdict changes after it is written. What a gate made of it is
  in the gate's status (section 6.5).
- **`result`** is an enum. `summary` is required for `fail` and `inconclusive`
  (CEL rule: `self.result == 'pass' || size(self.summary) > 0`): a blocked promotion must say why.
- **The model is asserted.** It lives in `actor.asserted.model`, with the session and client, as
  17-actors §4.1 defines. `startedAt` and `durationSeconds` are asserted too. They are shown and
  exported, never used for a decision.
- **Printer columns**: Bundle, Env, Check, Result, By, Age.

Why not reuse Approval with `decision: pass|fail|inconclusive`? See decision V1 (section 12).

## 4. Identity admission

### 4.1 The `<release>-verdicts` policy

A ValidatingAdmissionPolicy in `chart/kardinal-promoter/templates/identity-admission.yaml`, the
same shape as `<release>-approvals`, bound with the same `identityBindings` helper (so it
covers exactly the namespaces this release manages, and exempts nobody it should not):

| Operation | Rule | Same as Approvals |
|-----------|------|-------------------|
| CREATE | `spec.by == request.userInfo.username` | `spec.user` |
| CREATE | `spec.groups` all in `request.userInfo.groups` | yes |
| CREATE, UPDATE | labels `kardinal.io/bundle`, `kardinal.io/environment`, `kardinal.io/check` equal `spec.bundle`, `spec.environment`, `spec.check` | bundle and environment |
| CREATE | the only owner may be the Bundle `spec.bundle` with UID `spec.bundleUID` | yes |
| UPDATE | ownerReferences unchanged (except by the garbage collector) | yes |
| CREATE | `spec.run` set only from the AgentRun's own Pod (4.2) | new |
| DELETE | only `oldObject.spec.by`, the garbage collector and the namespace controller | yes (no UI writer: the UI does not write Verdicts in v0.12) |

The spec is immutable by CRD rule, so only CREATE checks the identity fields.

### 4.2 Binding a Verdict to its AgentRun

A gate counts each AgentRun as its own vote (section 6.3), so a Verdict that names a run must
really come from that run. Admission has no lookup (17-actors §5.1), but it does see the
requesting Pod: a bound ServiceAccount token, which every projected Pod token is, adds
`authentication.kubernetes.io/pod-name` to `request.userInfo.extra`
(`k8s.io/apiserver pkg/authentication/serviceaccount/util.go:38-48`). The AgentRun reconciler
names the Job after the AgentRun, and the Job controller names its Pods `<job>-<5 random>`.

```cel
!has(object.spec.run) || (
  'authentication.kubernetes.io/pod-name' in request.userInfo.extra &&
  request.userInfo.extra['authentication.kubernetes.io/pod-name'].exists(p,
    p.startsWith(object.spec.run.name + '-')) &&
  object.metadata.name == object.spec.run.name)
```

- A run's Verdict is named after the run, so a run has at most one Verdict: a second CREATE is
  `AlreadyExists`.
- A token minted with `TokenRequest` outside a Pod has no `pod-name`, so it cannot claim a run.
- A Job's Pod name keeps the full Job name only while the Job name is at most 52 characters
  (the Job controller cuts `generateName` to 58). AgentRun names are therefore at most 52
  characters (`boundedName`, as `HookRunName`).
- What it does not stop: someone who may create Pods with the agent's ServiceAccount, named like
  the run, in that namespace. That is someone who can already act as the agent (17-actors §11).

### 4.3 The actor stamp

The actor policy of 17-actors §5.3 (`<release>-actor`, ENGINEER-1's #1834) gains `verdicts` in
its `matchConstraints` (CREATE). Its stamp and scope checks apply unchanged:

- An agent must stamp; its stamp must say what the AgentIndex says; a suspended or not-ready
  agent is denied.
- Scope: the Verdict's namespace, `spec.pipeline` and `spec.environment` must be in the agent's
  `scope`. `spec.pipeline` is not checked against the Bundle (admission cannot read the Bundle),
  so the Graph also filters on it (section 5.1): a Verdict that names another Pipeline to pass a
  scope check is never counted.
- The verified stamp principal equals `spec.by` (17-actors §4.1: "the v0.11 names stay and must
  agree with the stamp").

A human or a bot may write a Verdict without a stamp (17-actors §5.5). It reads as
`kind: unknown` with `principal: spec.by`.

### 4.4 RBAC

The chart's user roles: `<release>-approver` gains `create`, `delete` on `verdicts` (the people who
may approve may verify). A new `<release>-verifier` ClusterRole grants `get, list, watch, create`
on `verdicts` only, for binding to agent and CI ServiceAccounts. Viewer roles gain `get, list,
watch`. The Graph ServiceAccount gains `list, watch` on `verdicts` (selector ref), and the Graph applier role every verb on `agentruns` (it
creates them and reads them back), in `graph-rbac.yaml`.

## 5. How the Graph reads Verdicts

### 5.1 A selector `ref` node, as for Approvals

`pkg/graph/approvals.go` already reads Approvals with a selector `ref` on the
`kardinal.io/bundle` label, and the approval gate collection copies the eligible ones into each
gate instance's `spec.approvals` (`pkg/graph/gates.go` `approvalsOfItem`). Verdicts use the same
pattern:

```yaml
- id: Verdicts                       # uppercase: no environment node can take it
  ref:
    apiVersion: kardinal.io/v1alpha1
    kind: Verdict
    metadata:
      namespace: payments
      selector:
        matchLabels: {kardinal.io/bundle: checkout-1-5-0}
```

It is added only to Graphs with a verify gate. Each verify gate instance's `spec.verdicts` is:

```cel
Verdicts.filter(v,
    v.spec.bundleUID == bundle.metadata.uid &&      # this very Bundle, not an earlier one of the name
    v.spec.pipeline == "checkout" &&                 # literal (4.3)
    v.spec.environment == <verify.environment> &&
    v.spec.check == <verify.check> &&
    <eligible(v)>)                                   # allowedAgents / allowedUsers / allowedGroups (6.2)
  .sortBy(v, v.metadata.name)
  .map(v, {"name": v.metadata.name, "createdAt": v.metadata.creationTimestamp,
           "result": v.spec.result, "summary": v.spec.?summary.orValue(""), "by": v.spec.by,
           ?"groups": v.spec.?groups, ?"actor": v.spec.?actor, ?"run": v.spec.?run,
           ?"evidenceURL": v.spec.?evidenceURL,
           "findings": size(v.spec.?findings.orValue([])),   # counts only: 101 Verdicts of 50
           "highFindings": size(<the high and critical ones>)})  # findings each would not fit
```

capped at 101 entries like `spec.approvals` (more than 100 holds the gate: "more than 100
verdicts; ..."). Eligibility is filtered before the cap, so Verdicts from writers who do not
count cannot crowd out the ones that do, as for Approvals.

`creationTimestamp` is copied because it is written by the API server: the timeout (6.4) and the
latest-vote rule (6.3) use it, and nothing the writer controls.

Gate instances that read approvals or verdicts are one collection (today's `ApprovalGates`): a
gate with both `approval` and `verify` waits for both. Its template renders `spec.verdicts` only
when the Graph has the `Verdicts` node.

### 5.2 What kro does here (verified)

| Behaviour we depend on | kro v0.10.0-rc.0 |
|------------------------|------------------|
| A selector ref with no match is an empty, ready list, not data-pending | `executor/simple.go:1235-1301` `applyRefCollection` lists and publishes whatever matches |
| A new or deleted Verdict re-renders the gates | `simple.go:1268-1286`: a selector watch is registered before the list, keyed by the node ID |
| The watch needs the Graph identity to `watch` the kind in the namespace | `simple.go:1440-1456` `skipWatchForIdentity`; without the grant the read still works but drift is lost. A selector watch has no name, so G18 does not apply; the grant is in `graph-rbac.yaml` (4.4) |
| Optional map entries (`?"key": v.spec.?field`) | `pkg/cel/environment.go:134` `cel.OptionalTypes()` |
| `sortBy`, `filter`, `map` on the list | `environment.go:130-131` (`ext.TwoVarComprehensions`, `ext.Lists`) and the kro list library; `approvalsOfItem` uses them on main |

No new kro gap. Ledger G11 applies as it does to approval gates: the collection is
all-or-nothing on pending data, so every field the template reads is present in every item.

## 6. The verify gate

### 6.1 Shape

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: prod-needs-exploratory-qa
  namespace: platform-policies
  labels: {kardinal.io/scope: org, kardinal.io/applies-to: prod}
spec:
  expression: "true"                 # a verify-only gate; or any CEL, see verdicts.* (6.6)
  message: prod needs two passing exploratory QA runs in uat
  verify:
    check: exploratory-qa            # required, DNS label
    environment: uat                 # the verified environment; default: the gated environment's
                                     #   only direct upstream (a build error when it has none or several)
    required: 2                      # votes that must pass; 1-20, default 1
    maxFailures: 0                   # counted fails tolerated; 0-20, default 0 (any fail blocks)
    allowedAgents: [qa-explorer]     # agents whose verdicts count; agents count ONLY when listed (opt-in)
    allowedUsers: []                 # people and bots whose verdicts count
    allowedGroups: []                #   (both empty: any non-agent with RBAC on verdicts)
    excludeAuthor: true              # default true: the version's author party never counts (6.2)
    onInconclusive: retry            # hold | retry | fail; default hold
    retries: 1                       # with retry: further attempts per AgentRun; 1-5, default 1
    timeout: 2h                      # from the verified environment's health check; "" = no timeout
    onTimeout: fail                  # fail | allow; default fail
    run:                             # optional: run verifiers in the cluster (section 8)
      template: qa-explorer          # a StepTemplate
      count: 3                       # AgentRuns per version; 1-10, default = required
```

The translator validates it when it builds the Graph (`pkg/graph/validate.go`): `environment` must
be an upstream (direct or not) of every environment the gate applies to, the gate may not be a
skip-permission gate (like approvals), `run.count >= required`, and `retries` needs
`onInconclusive: retry`.

A verify gate on the verified environment itself can never pass (the version is not there yet
when that environment's gates are evaluated), so `environment` equal to the gated environment is
refused.

### 6.2 Eligibility and the one-party rule

A Verdict in `spec.verdicts` is a **vote** when all of these hold, in this order (the first that
fails is the reason it does not count, shown in the gate status):

1. **Its writer may verify.**
   - An agent (`actor.kind == agent`) only when `actor.agent` is in `allowedAgents`. There is no
     wildcard: agent verification is opt-in per agent, as agent approvals are (E7).
   - Anyone else when `spec.by` is in `allowedUsers` or a `spec.groups` entry is in
     `allowedGroups`; with both empty, anyone but an agent.
   The Graph applies this filter already (5.1); the reconciler applies it again, so the CLI and
   the UI explain it with the same function.
2. **One party.** With `excludeAuthor: true`, a Verdict does not count when
   `actor.SameParty(author, verifier)`: the version's author (its Bundle's stamp, or
   `kardinal.io/created-by` when unstamped), the author's agent and that agent's sponsor are one
   party, and none of them verifies the others' work. **For an agent verifier the rule applies
   whether or not `excludeAuthor` is set**: an agent never verifies work of its own party (the
   same rule as E7's agent approvals, 17-actors §10.3). An author with no known principal holds
   the gate with `excludeAuthor` ("the author is unknown"), as approvals do.
3. **Not superseded** (6.3).
4. **Before the deadline** (6.4).

Test cases (section 13) include the author agent's verdict (refused by rule 2), the sponsor's
own agent verifying the sponsor's version (refused by rule 2), and a Verdict for another
version's UID (never copied, 5.1).

### 6.3 Counting: one vote per voter

The voter of a Verdict is:

- its **run** (`spec.run.index`: every attempt of run 2 is one voter) when `spec.run` is set: admission proved it came from that run's Pod (4.2), and each run is
  controller-created, so N runs are N independent samples;
- otherwise its **party key** (`actor.PartyKey`: the sponsor for an agent, the principal for anyone
  else). An external agent or CI job therefore has one vote, however many Verdicts it writes.

Each voter's **latest** Verdict (by `createdAt`, then name) is its vote; earlier ones are recorded
as `superseded by <name>`. Then:

| Outcome | When | Gate |
|---------|------|------|
| **Failed** | more than `maxFailures` votes are `fail` (or `inconclusive` with `onInconclusive: fail`) | blocked |
| **Verified** | at least `required` votes are `pass`, and not Failed | passes (if the expression is true) |
| **Timed out** | past the deadline (6.4), neither of the above | `onTimeout: fail` blocked, `allow` passes |
| **Waiting** | otherwise | blocked |

`inconclusive` with `hold` or `retry` is not a vote for anything: it waits. Failed wins over
Verified, so a gate never passes while it has more fails than it tolerates.

Why one vote per run, and one per party otherwise: section 12, V3.

### 6.4 Timeout

The deadline is the verified environment's `Bundle.status.environments[].healthCheckedAt` plus
`timeout`: the moment the version was verified healthy there, written by the Bundle reconciler to
its own status. The gate reads it from the Bundle it already reads for `bundle.*`
(`pkg/reconciler/policygate`, `buildContext`). No new clock source: like soak time, the gate
compares a CRD timestamp with its evaluation time, and requeues itself for the instant the
deadline passes (as for override expiry).

A Verdict created after the deadline does not count (`after the timeout`). That keeps the outcome
stable: once Timed out, a late pass does not quietly flip a failed gate. A person who wants the
late result can override the gate (`kardinal override`), which is audited.

`onTimeout: allow` is for checks that are advisory after a while ("explore for up to an hour; if
the agent is stuck, do not hold the release"). It is explicit, and its reason says so.

### 6.5 Status and the reason line

The gate records each Verdict in `status.verdicts`, like `status.approvals`:

```yaml
status:
  verdicts:
    - name: checkout-1-5-0-uat-exploratory-qa-r1-a1
      verifier: agent qa-explorer for bob@example.com     # actor.Describe (17-actors §8)
      result: pass
      counted: true
      run: 1
      attempt: 1
      summary: Checkout, refunds and the coupon flow work; 41 pages visited.
      evidenceURL: https://ci.example.com/runs/88123
      findings: 1
      createdAt: "2026-10-14T10:09:03Z"
    - name: ...
      result: inconclusive
      counted: false
      reason: inconclusive (retrying: attempt 2 of 2)
```

`status.reason` (the same sentence `kardinal explain`, the UI, the PR and notifications show):

| Outcome | Reason |
|---------|--------|
| Waiting | `waiting for verdicts: 1 of 2 (qa-explorer)` |
| Waiting, with an inconclusive | `waiting for verdicts: 1 of 2 (qa-explorer); 1 inconclusive, retrying` |
| Verified | `verified: 2 of 2 (qa-explorer ×2)` |
| Failed | `verification failed: qa-explorer: Checkout returns 500 for coupon SAVE10` |
| Timed out, fail | `verification timed out after 2h: 1 of 2 (qa-explorer)` |
| Timed out, allow | `verification timed out after 2h, allowed by onTimeout: 1 of 2 (qa-explorer)` |

The names in parentheses are the passing voters' agents or principals, in order, with `×N` for
repeats. The version ends the reason, as for every gate (`graph.WithVersion`).

Each Verdict that appears in, or leaves, a gate's `spec.verdicts` writes an AuditEvent
(`VerdictRecorded`, `VerdictRevoked`) through the gate's audit outbox, as approvals do
(`recordApprovals`), with `actor` set to the Verdict's stamp (17-actors §7).

### 6.6 CEL: `verdicts.*`

For expressions that want more than the verify block (the CEL-only rule: these are variables,
not functions):

| Variable | Type | Meaning |
|----------|------|---------|
| `verdicts.passed` | int | pass votes |
| `verdicts.failed` | int | fail votes (inconclusive counted as fail with `onInconclusive: fail`) |
| `verdicts.inconclusive` | int | inconclusive votes that wait |
| `verdicts.required` | int | `verify.required` |
| `verdicts.verifiers` | list(string) | passing voters, as in the reason |
| `verdicts.timedOut` | bool | past the deadline |
| `verdicts.findings.high` | int | findings of severity high or critical in counted votes |

Example: `verdicts.findings.high == 0` on top of the quorum. A gate without `verify` gets zeros
(`verdicts.required == 0`).

### 6.7 How the gate composes

Like `approval`, `verify` holds a gate whose expression passes; an expression that is false blocks
first, with its message. An active override passes the gate as today. A gate may have both
`approval` and `verify`: both must be met, approvals first in the reason.

## 7. `kardinal verify`

```
kardinal verify <bundle|version> --pipeline P --env uat --check exploratory-qa
                (--pass | --fail | --inconclusive) [--summary TEXT] [--evidence URL]
                [--finding 'high:Title']... [--version V] [--revoke [--name NAME]]
kardinal verify list <bundle|version> --pipeline P [--env E] [--check C]
```

- Resolves the version like `kardinal approve` (a Bundle name, or a version of the Pipeline).
- Writes `spec.by` and `spec.groups` from a SelfSubjectReview, and the actor stamp as 17-actors §6
  says (`actor.Derive` on the AgentIndex; asserted fields from `KARDINAL_ACTOR_MODEL`, `_SESSION`
  and `_CLIENT`, as for every other command).
- Owns the Verdict by the Bundle, sets the three labels, and names it
  `<bundle>-<env>-<check>-<hash of by and time>`.
- After writing it reads the gates that wait for this check and tells the caller whether each
  counts the verdict, from the same functions the reconciler uses (`lifecycle.VerdictStandingOf`):
  "not counted by gate prod-needs-qa: the author's party (agent fixer for alice)". A verdict that
  does not count is still written: it is evidence.
- `--fail` and `--inconclusive` require `--summary`.
- `--revoke` deletes the caller's latest Verdict for (version, env, check), or `--name`.
- `verify list` prints NAME, ENV, CHECK, VERIFIER, RESULT, COUNTED (with the reason), AGE and
  EVIDENCE, read from the Verdicts (what exists) and the gates' `status.verdicts` (what counted).

The local MCP server (E10) exposes the same as a tool, so a desktop agent submits its verdict
through it.

## 8. AgentRun: verifiers in the cluster

### 8.1 Options

What runs an agent image against the environment under test, once per version, and turns its
result into a Verdict?

**(a) Extend HookRun.** A third hook phase, `verify`, on Pipeline environments, whose Job writes a
Verdict.
- Pro: HookRun already solves G12 (it owns its Job, so garbage collection deletes the Pods),
  latches its result once, ignores spec changes after start, and checks Pod security.
- Con: a hook is a Pipeline (team) object with a full user `JobSpec`: any volume, any Secret,
  any ServiceAccount on the `--hook-service-accounts` list. The bounds this feature needs (an
  admin-registered image, a controller-built Pod, no git credentials, a NetworkPolicy, an agent
  identity) would be a second, stricter mode inside one kind. A hook's result gates its own
  environment (`post`: Verified only when it succeeds); a verifier's result gates the next one.
  HookRuns have no run index, no attempts, and no link to a Verdict.

**(b) A new AgentRun CRD, with its registration in a new kind.** A namespaced, Graph-owned run
object whose reconciler builds the Pod itself from a cluster-scoped registration.
- Pro: the bounds are the only mode; the reconciler follows HookRun's Job lifecycle without its
  API; runs and attempts are first-class.
- Con: one more Graph-owned kind (admission `graph-objects`, RBAC, CRD docs), and a registration
  kind to keep.

**(c) A generic StepTemplate now.** The planned v0.14 "custom steps" (AGENTIC-PLAN §5): admin
images with input and output schemas at hook points (`pre`, `after-update`, `before-pr`,
`post`), of which a verifier is one.
- Pro: one mechanism for all custom steps.
- Con: the git-workspace steps need the promotion Job sandbox (the RenderRun pattern), output
  schemas and placement at four hook points: weeks of design that v0.12 does not need, before
  the first verifier runs.

**Decision: (b), with the registration named and shaped as StepTemplate's v0.12 subset.** The
run object is a new `AgentRun` kind (b). The registration is the cluster-scoped `StepTemplate`
kind (c), with `output: Verdict` the only output v0.12 accepts. v0.14 adds outputs, schemas and
hook points to the same kind, so registrations do not migrate. The AgentRun reconciler follows HookRun's
Job lifecycle (create with an ownerReference, observe, latch, deadline, adopt) in its own, smaller
code: an AgentRun has no finalizer, spec hash or sibling ordering, which is most of HookRun's.

### 8.2 StepTemplate (cluster-scoped, admin-registered)

```yaml
apiVersion: kardinal.io/v1alpha1
kind: StepTemplate
metadata:
  name: qa-explorer
spec:
  description: Explores the web app like a QA engineer and reports.
  output: Verdict                    # v0.12: the only value
  image: ghcr.io/acme/qa-explorer@sha256:4f1c...   # a digest is recommended; --step-template-require-digest refuses tags
  command: ["/qa-explorer"]
  args: ["--depth", "3"]
  env:                               # literal values only
    - {name: MODEL_ENDPOINT, value: https://llm.internal.example.com}
  secretEnv:                         # values from Secrets in the Pipeline namespace (8.4)
    - {name: MODEL_API_KEY, secret: qa-explorer-model, key: token}
  serviceAccountName: qa-explorer    # in the Pipeline namespace; must be an agent (8.4)
  timeout: 45m                       # per attempt; default 30m, at most 24h
  resources: {requests: {cpu: 250m, memory: 256Mi}, limits: {cpu: "1", memory: 1Gi}}
  egress:                            # beyond DNS, the API server and the target namespace
    - cidr: 10.20.0.0/16             # the model endpoint
      ports: [443]
  runtimeClassName: gvisor           # optional: a sandboxed runtime (8.6)
status:
  ready: true                        # the StepTemplate reconciler validates; reason otherwise
```

Only cluster admins write StepTemplates: the chart's user roles do not include them, and
viewers may read them.

### 8.3 The AgentRun object and its place in the Graph

For each verify gate with `run`, the translator adds `count` runs per (verified environment,
check, template), each with up to `1 + retries` attempts (`retries` only with
`onInconclusive: retry`). Two gates that ask for the same (environment, check, template) share
the runs, with the larger count.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentRun
metadata:
  name: checkout-1-5-0-uat-exploratory-qa-r2-a1    # boundedName, at most 52 characters (4.2)
  namespace: payments
  labels: {kardinal.io/pipeline: checkout, kardinal.io/bundle: checkout-1-5-0,
           kardinal.io/bundle-uid: 1b2c..., kardinal.io/environment: uat,
           kardinal.io/check: exploratory-qa, kardinal.io/agentrun-run: "2"}
spec:
  pipelineName: checkout
  bundleName: checkout-1-5-0
  bundleUID: 1b2c...
  version: 1.5.0
  environment: uat                   # verified environment
  targetNamespace: checkout-uat      # what the agent tests (8.5)
  check: exploratory-qa
  template: qa-explorer
  run: 2
  attempt: 1
  recorded: ""                       # Graph-rendered: the result of this run's Verdict if one exists (8.7)
status:
  phase: Running                     # Pending | Running | Succeeded | Failed | Skipped (terminal: last three)
  message: ""
  jobName: checkout-1-5-0-uat-exploratory-qa-r2-a1
  jobUID: ...
  startedAt, deadline, finishedAt: ...
  conditions: [...]                  # Bounded (8.4), SpecChangedAfterStart
```

**Graph mechanism.** The same shape in both Graph shapes, the compact shape's pattern for
HookRuns (`pkg/graph/compact_extras.go`): data, admission, collection.

- `AgentRunData` (a `def` node): every run attempt as an item: `{name, step, run, attempt, prev}`,
  where `step` is the verified environment's PromotionStep name and `prev` the previous attempt's
  name.
- `refAgentRuns` (a selector `ref` on `kardinal.io/bundle-uid`) reads the AgentRuns back, and the
  `Verdicts` ref (5.1) reads their Verdicts. The steps are read back through `refVerifySteps` in the
  node shape and `StepsObserved` in the compact shape. `AgentRunState` (a `def`) lists the AgentRuns
  that exist and that finished, the runs a pass or fail decided, and the Verified steps.
- `AgentRunsAdmitted` (a `def`) keeps an item when its AgentRun already exists, or when all of:
  the Bundle is not Superseded or Rejected (`bundleHeld`); the verified environment's step is
  `Verified`; and either `attempt == 1`, or the previous attempt is terminal and its Verdict is
  missing or `inconclusive` (the retry rule, 9.1).
- `AgentRuns` (a collection per `MaxCollectionItems`) creates one AgentRun per admitted item.
  Its `readyWhen` is `${each.?status.?phase.orValue("") in ["Succeeded", "Failed", "Skipped"]}`:
  the UI's run state. Nothing waits on it: the gate waits on Verdicts.

So the retry decision is made in the Graph, from CRD fields (the previous AgentRun's status and its
Verdict). The AgentRun reconciler never reads a Verdict to decide anything.

Checked against kro v0.10.0-rc.0: a `def` over selector-ref data and a collection over the
`def` is what the compact shape's HookRuns already do on main. An item stays admitted once its
object exists, so a later change never prunes a run in flight (G11: a collection's items leave
only when the def drops them). A template node re-creates an object that disappears; for a
run-once kind that would run it again, which `spec.recorded` prevents (8.7). The admission
`graph-objects` policy gains `agentruns`, so only kro and the controller create them (G17).

### 8.4 The AgentRun reconciler: bounds

`pkg/reconciler/agentrun` writes only the AgentRun's status, and creates two children it owns:
a NetworkPolicy and a Job. Before the first, it checks the run is **bounded**, and fails it
(phase `Failed`, condition `Bounded=False` with the reason) otherwise:

1. **The template is ready**, and `output` is `Verdict`.
2. **The ServiceAccount is an agent.** `system:serviceaccount:<ns>:<serviceAccountName>` is in
   the AgentIndex (17-actors §5.4), not blocked, and its scope allows the namespace, the Pipeline
   and the verified environment. Read once per start from the AgentIndex the controller builds
   (its own CRD's status, read through the cache).
3. **Its RBAC is read-only.** SubjectAccessReviews as that ServiceAccount: it may `create
   verdicts` in the Pipeline namespace; it may not `get` or `list` secrets, `create pods/exec`, nor
   `create`, `patch` or `delete` deployments, statefulsets, daemonsets, pods, services or configmaps,
   in the Pipeline's namespace or the target namespace; nor `create`, `patch` or `delete` bundles,
   approvals, policygates or pipelines, nor `create serviceaccounts/token`, in the Pipeline's. The list is
   in code, with a test per verb. A wide grant fails closed: "agent qa-explorer may patch
   deployments in checkout-uat; an AgentRun's agent must be read-only".
4. **Its Secrets are its own.** Every `secretEnv` Secret carries the label
   `kardinal.io/step-template: <template>`. A Pipeline's git or SCM Secret is not labelled for a
   template, so a template cannot pull git credentials into the Pod even by name.

Then it creates, with the AgentRun as controller owner:

- **NetworkPolicy** `<run>`: selects the run's Pod; no ingress; egress to DNS (kube-dns,
  53/UDP and TCP), the API server (the `default/kubernetes` EndpointSlice addresses and port,
  read at start), every Pod in the target namespace, and the template's `egress`. Nothing else:
  no cloud metadata, no other namespace, no git host. Enforced where the CNI implements
  NetworkPolicy (kind's kindnet does from kind v0.24; the live test asserts a blocked address).
- **Job** `<run>`: one Pod, `restartPolicy: Never`, `backoffLimit: 0`,
  `activeDeadlineSeconds` = the timeout, `ttlSecondsAfterFinished` unset (the AgentRun owns it).
  The Pod is built by the controller, never from user Pod YAML: the template's image, command,
  args, env and resources; `serviceAccountName`; `automountServiceAccountToken: false` and a
  projected token at the standard path with `expirationSeconds` covering the timeout (so
  in-cluster clients work, and the token dies with the run); `runtimeClassName` when set; Pod
  security `restricted` (non-root, no privilege escalation, all capabilities dropped,
  `RuntimeDefault` seccomp, read-only root filesystem with an `emptyDir` at `/tmp`); no other
  volumes but the context (8.5).

The run is **Succeeded** when the Job completes, and **Failed** when it fails, passes its deadline
(the Job is deleted, as HookRun does), or is refused as unbounded. The result of the check is in
the Verdict, not in the phase: a Job that exits 0 without writing a Verdict is Succeeded with
"finished without a verdict", and counts as inconclusive for the retry rule (9.1).

The "read-only kubeconfig scoped to the target namespace" of the plan is this: the Pod's only
credential is a short-lived token of a ServiceAccount whose RBAC check 3 proved read-only, and
whose NetworkPolicy lets it reach only the API server, the target namespace and the model.

### 8.5 The contract: inputs and output

**Inputs**, as environment variables and as one JSON file (the same content):

| Variable | Example |
|----------|---------|
| `KARDINAL_PIPELINE`, `KARDINAL_BUNDLE`, `KARDINAL_BUNDLE_UID`, `KARDINAL_VERSION` | `checkout`, `checkout-1-5-0`, `1b2c...`, `1.5.0` |
| `KARDINAL_ENVIRONMENT`, `KARDINAL_TARGET_NAMESPACE` | `uat`, `checkout-uat` |
| `KARDINAL_CHECK`, `KARDINAL_RUN`, `KARDINAL_ATTEMPT` | `exploratory-qa`, `2`, `1` |
| `KARDINAL_IMAGES` | JSON list of the version's images (repository, tag, digest) |
| `KARDINAL_DEADLINE` | RFC 3339 |
| `KARDINAL_CONTEXT` | `/kardinal/context.json` |

`/kardinal/context.json` is a downward-API file of the Pod annotation `kardinal.io/context`, so no
ConfigMap is created. It holds the variables above, the version's provenance, and the
**Verdict to write**, complete but for `result`, `summary`, `evidenceURL`, `findings`,
`startedAt` and `durationSeconds`: name, namespace, labels, owner, `pipeline`, `bundle`,
`bundleUID`, `environment`, `check`, `by`, `run`, and the actor stamp the AgentIndex gives the
ServiceAccount (`actor.Derive`).

`targetNamespace` is the verified environment's workload namespace: the `resource` health check's
namespace when set, else the namespace that check defaults to; `verify.run.namespace` overrides
it.

**Output**: exactly one Verdict, created with the Pod's own token (any client: `kubectl create -f`,
client-go, or `kardinal verify --from-context`). The image's exit code says whether it ran; the
Verdict says what it found. An agent that finds a bug exits 0 and writes `fail`.

The repository ships a minimal reference agent, `hack/e2e/testagent` (a Go program, standard
library only: probe the target namespace's Services over HTTP, then write a Verdict). The live e2e builds it, loads it
into kind, and registers it as a StepTemplate.

### 8.6 Sandboxing: options

| Option | What | v0.12 |
|--------|------|-------|
| `runtimeClassName` on the Pod | gVisor or Kata through a RuntimeClass (kubernetes-sigs/agent-sandbox installs them) | **Yes**: one field, works with any Job |
| A `Sandbox` CR (`agents.x-k8s.io`) instead of a Job | agent-sandbox's long-lived, stateful singleton Pod with a stable identity | No: a verifier is run-to-completion; a Sandbox has no completion or deadline, and the reconciler would need a second lifecycle. Revisit when agent-sandbox adds a run-to-completion mode |
| A `SandboxTemplate` / `SandboxClaim` | agent-sandbox's warm pools | No: same, plus a CRD dependency the chart does not have |

### 8.7 Run once

Each attempt runs at most once:

- The reconciler latches a terminal phase; the CRD refuses a change from a terminal phase (as
  HookRun's `phase` rule).
- A Job of the run's name with another UID, or a missing Job while Running, fails the run: it is
  not run again (HookRun's rule).
- If kro re-creates a deleted AgentRun (a template node re-creates what disappears), the Graph
  renders `spec.recorded` from the `Verdicts` ref: the result of the Verdict named after the run.
  An AgentRun that starts with `recorded` set does not run its Job; it is Succeeded with
  "verdict already recorded". This is the HookRun `spec.recorded` pattern, with the Verdict as
  the record, and keeps the decision on CRD fields the Graph renders.

## 9. Retries and flakiness

### 9.1 What is retried

| Event | Gate with `hold` | with `retry` | with `fail` |
|-------|------------------|--------------|-------------|
| Verdict `inconclusive` | waits | the Graph admits the next attempt, up to `retries` | a fail vote |
| AgentRun ends without a Verdict (crash, deadline, refused) | waits | next attempt | waits (no vote) |
| Verdict `fail` | a fail vote | a fail vote (never retried) | a fail vote |
| Verdict `pass` | a pass vote | a pass vote | a pass vote |

A run whose last attempt ends without a Verdict leaves its voter silent. With a `timeout`, the gate
then times out as configured; without one it waits, and `kardinal explain` says which runs ended
without a verdict.

A fail is never retried: retrying fails until one passes would turn N-of-M into "keep asking".
Tolerating fails is explicit, with `maxFailures`.

### 9.2 Patterns

| Want | verify |
|------|--------|
| One agent, must pass | `required: 1, run: {count: 1}` |
| 2 of 3 runs, one bad run tolerated | `required: 2, maxFailures: 1, run: {count: 3}` |
| Flaky agent: inconclusive is retried once | `onInconclusive: retry, retries: 1` |
| An external agent and a person must both pass | two gates, one `allowedAgents: [x]`, one `allowedUsers: [...]` |
| Advisory exploration | `required: 1, timeout: 1h, onTimeout: allow` |

### 9.3 Measuring flakiness

The gate reconciler counts each Verdict it first records in
`kardinal_verdicts_total{check, result, verifier_kind}` and each AgentRun end in
`kardinal_agentruns_total{template, phase}`. The inconclusive share per template is the
flakiness figure the docs show how to graph. Per-agent DORA (v0.13, earned autonomy) reads the
same records.

## 10. Evidence

- **PR body.** A `### Verification` section, after the gate table, for each verify gate of the
  environment: one row per Verdict in `status.verdicts`: verifier sentence, result, counted (or
  why not), summary, evidence link, findings count, run and attempt. Built by the PromotionStep
  reconciler from the gates it already reads for the gate table (`collectGateResults`), so no new
  read. The custom PR template gets `.Verifications`.
- **UI.** The stage drawer's gate section shows a verify gate's quorum (`1 of 2`) and each
  Verdict: the verifier sentence, the result, its run and attempt, whether it counted and why
  not, the summary, the number of findings and the evidence link, with the `kardinal verify`
  command. The UI API returns the gate's `verify` policy and `status.verdicts`.
- **CLI.** `kardinal explain` prints the verify gate's reason and its Verdicts; `kardinal verify
  list` (section 7).
- **Audit.** `VerdictRecorded` and `VerdictRevoked` AuditEvents with the Verdict's actor (6.5);
  `kardinal audit export` carries them. The signed receipt (v0.13) includes each counted
  Verdict's name, result, verifier and evidence URL.

## 11. What this does not protect against

- **A compromised verifier.** A verdict is as good as its verifier. An agent with a stolen or
  shared ServiceAccount can pass anything it is allowed to verify. Quorum across different
  agents (two gates, or `allowedAgents` with several agents) and `maxFailures: 0` reduce that.
- **Prompt injection from the environment under test.** An exploratory agent reads what the app
  serves, and the app is the change under test. A malicious change could talk the agent into a
  pass. kardinal bounds what the agent can do (8.4), not what it concludes. Use more than one
  agent and a human approval for prod.
- **Asserted fields.** Model, session, `startedAt` and `durationSeconds` are claims.
- **NetworkPolicy without an enforcing CNI** does nothing. kardinal cannot detect whether the CNI
  enforces it, so the run still starts; the user page says so.
- **Read-only is checked, not guaranteed.** The SubjectAccessReviews cover a fixed list (8.4).
  A custom resource writable by the agent is not on it. Admins own the agent's RBAC.

## 12. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| V1 | Record kind | (a) Approval with more decisions; (b) new Verdict kind; (c) PolicyGate status written by agents | **(b)**: a verdict has a check, a run, findings and evidence that an Approval has not, and is for the *verified* environment, not the gated one. (a) would make every approval gate parse verdict fields; (c) is a cross-CRD status write by a client. |
| V2 | How the gate reads them | (a) selector ref + copy into the gate (Approvals' pattern); (b) the PolicyGate reconciler lists Verdicts; (c) a `def` that tallies in Graph CEL | **(a)**: the data reaches the gate through CRD fields, kro watches them, and it is already proven for Approvals. (b) is a cross-CRD read the Graph can do; (c) cannot do the one-party rule, the latest-vote rule or the timeout clearly in one expression, and loses the explanation the reconciler writes. |
| V3 | What one vote is | (a) one per party; (b) one per Verdict; (c) one per run when the Verdict is bound to a run, else one per party | **(c)**: N-of-M runs of one agent is the non-determinism this feature is for, and admission proves a run-bound Verdict came from that run's Pod (4.2). (a) makes 2-of-3 runs of one agent impossible; (b) lets any verifier vote twice by writing twice. |
| V4 | Repeated Verdicts from one voter | first counts; latest counts; any fail is final | **Latest counts**, with the earlier ones recorded and audited: a voter can always delete its own Verdict anyway, so "first" or "final" would only hide the change. Runs are not re-voted: each attempt is a new run attempt only after an inconclusive. |
| V5 | Where retries are decided | (a) AgentRun reconciler reads its Verdict and starts another Job; (b) the Graph admits the next attempt from the previous AgentRun's status and Verdict; (c) the gate reconciler creates AgentRuns | **(b)**: (a) is a reconciler deciding on another CRD (AGENTS.md anti-pattern); (c) is a gate creating objects. |
| V6 | Timer start | gate first evaluation; the verified environment's health check time; AgentRun start | **Health check time** (`healthCheckedAt`): a CRD timestamp that exists for external and in-cluster verifiers alike, and does not reset when a gate instance is re-created. |
| V7 | After the timeout | fail only; fail or allow; hold | **fail (default) or allow**: "hold" is no timeout. `allow` is explicit and named in the reason. |
| V8 | In-cluster runner | extend HookRun; new AgentRun; generic StepTemplate | **AgentRun + StepTemplate registration** (8.1). |
| V9 | Agent bounds | trust the image; NetworkPolicy + RBAC check + token + no-secret rule; a separate cluster | **NetworkPolicy + RBAC check + short token + labelled Secrets** (8.4): enforced by the API server and the CNI, not by the agent. |
| V10 | Sandbox | runtimeClassName; Sandbox CR; none | **runtimeClassName** (8.6). |
| V11 | Agents with empty allow lists | count; do not count | **Do not count**: agent verification is opt-in per agent, like agent approvals (the owner's rule). |

## 13. Test plan

Unit (table-driven, `-race`):

- `lifecycle.TallyVerdicts`: every row of 6.3 and 9.1; the cases below; 101 Verdicts; ties on
  `createdAt`.
- Admission (`<release>-verdicts` rendered and run against a real API server in the chart tests):
  `by` forged, groups forged, labels mismatched, foreign owner, `run` without a Pod token,
  `run` from another run's Pod, name not the run's, delete by someone else.
- Graph builder: the `Verdicts` node only with verify gates; the filter expression; AgentRun
  data, admission and collection in both shapes; validation errors (6.1).
- AgentRun reconciler: each bounds refusal (8.4), Job built restricted, NetworkPolicy shape,
  latch, deadline, re-created run with `recorded`, idempotency (a second reconcile writes
  nothing).
- CLI: `kardinal verify` stamps and standing messages with a fake client.

Fake-client journeys (`test/e2e/journeys_test.go` style), each a full Pipeline test→uat→prod:

| # | Case | Expect |
|---|------|--------|
| J1 | `required: 2`, `run.count: 3`, `maxFailures: 1`: pass, fail, pass | prod promotes; reason `verified: 2 of 2 (qa-explorer ×2)` |
| J2 | `onInconclusive: retry, retries: 1`: attempt 1 inconclusive, attempt 2 pass | attempt 2 admitted; prod promotes |
| J3 | the author agent's own Verdict | not counted: `the author's party`; prod waits |
| J4 | Bundle by alice; agent sponsored by alice passes | not counted; prod waits |
| J5 | a pass Verdict with another Bundle's UID | not copied; prod waits |
| J6 | one fail, `maxFailures: 0` | `verification failed: ...`; prod blocked |
| J7 | `timeout: 2m`, no Verdict, `onTimeout: fail` / `allow` | blocked with the timeout reason / passes with it |
| J8 | an agent not in `allowedAgents` | not counted |
| J9 | a person and an agent both pass, `required: 2` | counts 2 |

Live e2e on kind (cluster `kp-verify`, core suite, test prefix `TestGate_Verify`), COUNT=2,
with `coverage.tsv` rows:

- AgentRun end to end: the reference agent image built in the repo, registered as a
  StepTemplate, its AgentIdentity and read-only RBAC; `count: 2, required: 2`; both runs write
  pass; prod promotes; PR body has the Verification section.
- The run's Pod cannot reach an address outside its NetworkPolicy (the agent probes one and
  reports it in a finding).
- An unbounded agent (granted `patch deployments`) fails its AgentRun with `Bounded=False`.
- `kardinal verify --fail` by a person blocks prod; `--revoke` and a pass unblock it.
- Admission: a forged `by`, and a `run` claim from outside the Pod, are denied.

## 14. Out of scope here (follow-ups)

- Outputs other than `Verdict`, input and output schemas, and hook points for StepTemplate
  (v0.14 custom steps).
- An agent-sandbox `Sandbox` runtime (8.6).
- Requiring distinct sponsors across votes (`distinctParties`); today use several gates.
- Verdicts recorded through the UI.
- Re-running a finished check on demand (`kardinal verify --rerun`).
