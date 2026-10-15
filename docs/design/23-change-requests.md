# Design: Change requests and structured approval fields

> Status: Proposed (spec for v0.13.0, AGENTIC-PLAN §4). Implementation follows in separate PRs.
> Created: 2026-10-14
> Depends on: [17-actors.md](17-actors.md) (the actor stamp), [18-actor-policy.md](18-actor-policy.md)
> (the one-party rule, approvals), and the planned receipt spec `21-receipts.md` (the ticket and
> the fields are receipt inputs).
> Verified against: this repository at `main` (`pkg/reconciler/metriccheck`, `pkg/egress`,
> `pkg/reconciler/notificationhook`, `cmd/kardinal-controller/subscription_webhook.go`,
> `api/v1alpha1/approval_types.go`, `pkg/lifecycle/approval_tally.go`). The ServiceNow and Jira
> API facts in §5 come from the vendors' public REST documentation. They are **not** verified
> against a live instance yet; §5.4 says how the implementation verifies them.

---

## 1. Problem

Regulated teams cannot ship to prod without a change record: a ServiceNow change request, or a
Jira ticket in a change project. Today a team writes that link by hand, or uses a NotificationHook
that tells the ticketing tool about a promotion but cannot wait for its approval. Two things are
missing:

1. **A ChangeRequest gate.** For each promotion into an environment, kardinal opens a change
   request, or links an existing one, keeps its state in a CRD status, and holds the environment
   until the change request is approved. The ticket appears in the PR evidence and in the
   promotion receipt.
2. **Structured approval fields.** An approval that records only "approve" and a comment cannot
   carry what a change process asks for: a ticket ID, "I accept the risk of a schema migration",
   a rollback plan URL. Kargo's `get-user-input` step is the counterpart: a promotion step that
   waits for named inputs from a person.

| # | Feature | Section |
|---|---------|---------|
| C1 | `ChangeRequest` CR: opens or links a ticket, writes its state | 3, 4 |
| C2 | The gate reads it: `changeRequests.<name>.*` in CEL | 6 |
| C3 | ServiceNow and Jira providers, credentials, egress | 5 |
| C4 | Polling, webhooks, rate limits, idempotency | 7 |
| C5 | Structured approval fields: declared by the gate, given with the Approval | 8 |
| C6 | Evidence: PR body, AuditEvents, receipt | 9 |

## 2. Graph-first

Opening a ticket and reading its state are external HTTP calls. AGENTS.md forbids them in a gate's
hot path and in Graph CEL. The shape this repository already uses for external systems is the
**per-promotion MetricCheck**: a template with `perPromotion: true`, an instance the Graph creates
once the environment's upstreams are Verified (an owned node), a reconciler that calls the provider
and writes only its own status, and a gate that reads that status through the PolicyGate context
(`metrics.<name>`, ledger G8 "Cross-CRD reads for the gate context"). A ChangeRequest follows it
exactly:

| Concern | Mechanism |
|---------|-----------|
| The ticket per promotion | A `ChangeRequest` instance, an owned node of the Bundle's Graph, created from a template |
| External calls | The ChangeRequest reconciler only, through `pkg/egress` |
| State | `ChangeRequest.status` (the reconciler's own CRD) |
| The wait | A PolicyGate expression over `changeRequests.<name>`, read from status like `metrics.*` |
| Clock (staleness, timeouts) | Written to status by the ChangeRequest reconciler (`status.validUntil`, `status.timedOut`) |

No new G8 row: the gate's cross-CRD read joins the existing "Cross-CRD reads" row, and the
external calls join the side-effect class of NotificationHooks and MetricCheck providers.

## 3. Where the ticket lives: options

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Fields on the PolicyGate (`spec.changeRequest`), and the PolicyGate reconciler calls the ticketing API | One object | An HTTP call in the gate's reconcile (AGENTS.md anti-pattern); the gate would write ticket state into its own status from I/O; every recheck would call out |
| (b) | A new `ChangeRequest` CR, per-promotion instances created by the Graph from a template, its own reconciler | The MetricCheck pattern, proven; external I/O isolated in one reconciler with its own limiter and egress guard; status is evidence on its own | One more CRD |
| (c) | A MetricCheck provider (`provider: servicenow`) whose "value" is the ticket state | No new CRD | A change request is not a metric: it is *created* (a side effect), has an id, a URL, approvers and a lifecycle; overloading `threshold` and `value` hides them and makes the receipt unreadable |

**Choice: (b).** It isolates the side effect exactly where the MetricCheck reconciler isolates
queries, and its status is a self-describing record (ticket key, URL, state, approvers) that the
receipt and the PR body can cite.

## 4. The ChangeRequest CR

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeRequest
metadata:
  name: prod-change            # a template (perPromotion: true)
  namespace: checkout
spec:
  perPromotion: true           # the Graph creates one instance per Bundle and environment
  appliesTo: [prod]            # environments whose promotions need one
  provider: servicenow         # servicenow | jira
  mode: open                   # open | link
  servicenow:
    instanceURL: https://example.service-now.com
    credentialsSecretRef: {name: snow-kardinal, key: token}   # labelled kardinal.io/referenceable=true
    type: normal               # normal | standard | emergency (the Change Management API path)
    template: {short_description: "Promote {{ pipeline.name }} {{ bundle.version }} to {{ environment.name }}",
               assignment_group: "payments-cab"}              # string fields only, placeholders as MetricCheck
  jira:                        # alternatively
    baseURL: https://example.atlassian.net
    credentialsSecretRef: {name: jira-kardinal, key: token}
    project: CHG
    issueType: Change
    approvedStatusCategory: done          # or approvedStatuses: [Approved]
  link:                        # mode: link — where the ticket key comes from
    from: approvalField        # approvalField | bundleLabel | bundleAnnotation
    name: ticketID             # the structured approval field (§8), label or annotation key
  pollInterval: 1m             # default 1m, minimum 30s
  timeout: 24h                 # status.timedOut after it; the gate decides what that means
status:
  ready: true                  # the reconciler has a ticket and has read its state
  ticket:
    key: CHG0031337            # ServiceNow number, or Jira issue key
    id: 9f1c...                # sys_id or Jira issue id
    url: https://example.service-now.com/nav_to.do?uri=change_request.do?sys_id=9f1c...
  state: approved              # normalised: pending | approved | rejected | cancelled | closed | unknown
  providerState: "Scheduled / approved"   # the raw state(s), for people
  approvers: [{name: "jane@example.com", decision: approved, at: "..."}]   # when the API exposes them
  correlationID: kardinal:checkout/checkout-7f3c2a/prod/uid-…            # §7.3
  observedAt: "..."            # last successful read
  validUntil: "..."            # observedAt + 2 × pollInterval: older reads are stale
  timedOut: false
  conditions: [...]            # Ready, Stale, CredentialsInvalid, Refused (egress), ProviderError
  pendingAuditEvents: [...]    # ChangeRequestOpened, ChangeRequestApproved, ChangeRequestRejected
```

- **Instances.** The builder creates an instance per Bundle and environment from a template that
  applies to the environment, named like a per-promotion MetricCheck instance, labelled
  `kardinal.io/{pipeline,bundle,environment}`, created once the environment's upstreams are
  Verified (so a ticket is not opened for a version that never reaches the gate), and suspended
  when the Bundle stops promoting. This is `pkg/graph/metriccheck.go`'s mechanism, reused.
- **Templates and placeholders** are the MetricCheck set (`{{ bundle.version }}`,
  `{{ pipeline.name }}`, ...), rendered by the builder, string fields only (no free-form JSON from
  the template, so a template cannot set fields the provider maps).
- **Status only from the reconciler**; nothing else writes it (the chart's user roles grant no
  `changerequests/status`).

## 5. Providers

### 5.1 ServiceNow

| Operation | Endpoint (vendor docs) | Notes |
|-----------|------------------------|-------|
| Open | `POST /api/sn_chg_rest/change/{normal\|standard\|emergency}` (Change Management API) | Returns `sys_id`, `number` |
| Read | `GET /api/sn_chg_rest/change/{sys_id}` or `GET /api/now/table/change_request/{sys_id}?sysparm_fields=number,state,approval,...` | |
| Find by correlation | `GET /api/now/table/change_request?sysparm_query=correlation_id=<id>` | `correlation_id` and `correlation_display` are standard `task` fields for integrations |
| Approvers | `GET /api/now/table/sysapproval_approver?sysparm_query=sysapproval=<sys_id>` | Optional; needs read on the table |

State mapping (default; overridable per template):

| ServiceNow | kardinal `state` |
|------------|------------------|
| `approval` = `approved`, and `state` in Scheduled (-2) or Implement (-1) | `approved` |
| `approval` = `rejected` | `rejected` |
| `state` = Canceled (4) | `cancelled` |
| `state` = Closed (3) or Review (0) | `closed` |
| otherwise (New -5, Assess -4, Authorize -3, `approval` = requested / not yet requested) | `pending` |

The numeric `state` values are the defaults of the Change Management state model in the vendor
docs; instances customise them, so `servicenow.states` lets a template map raw values.

### 5.2 Jira

| Operation | Endpoint (vendor docs) | Notes |
|-----------|------------------------|-------|
| Open | `POST /rest/api/3/issue` (`/rest/api/2/issue` on Data Center) | Returns `id`, `key` |
| Read | `GET /rest/api/3/issue/{key}?fields=status,resolution` | `status.statusCategory.key` is `new`, `indeterminate` or `done` |
| Find by correlation | `POST /rest/api/3/search/jql` with `labels = "kardinal-<hash>"`, or an issue property | A label is the portable choice |

State mapping: `approvedStatuses` (exact status names) when set, else `statusCategory.key ==
approvedStatusCategory` (default `done`) is `approved`; `rejectedStatuses` (default none) is
`rejected`; a resolution of "Won't Do" or a configured set is `cancelled`; otherwise `pending`.
Jira has no native approval model outside Jira Service Management; teams model approval as a
workflow status, which is why the mapping is by status.

### 5.3 Credentials and egress

- **Secrets** follow the git-Secret rule: a `credentialsSecretRef` must name a Secret in the
  ChangeRequest's namespace labelled `kardinal.io/referenceable=true`, read uncached by name, as
  NotificationHook and MetricCheck secrets are. The controller's own SCM token Secret is refused.
- **Egress**: every request goes through `egress.NewTransport` (`pkg/egress/guard.go`): the
  deny list (loopback, link-local, metadata) always applies, and the controller allowlist
  (`--egress-allowlist`) when set. A refused destination is condition `Refused`, not a retry loop.
- **Auth**: ServiceNow basic auth or OAuth bearer; Jira Cloud email + API token (basic), Data
  Center PAT (bearer). The Secret key holds the header value's secret part only; the scheme is a
  spec field.

### 5.4 What is verified, and how

The endpoints, fields and default state values above are taken from the vendors' public docs and
are assumptions until checked. The implementation:

- records request/response fixtures from a ServiceNow developer instance (PDI) and a Jira Cloud
  test site, with credentials scrubbed, and runs the providers against them in unit tests;
- runs a fake ServiceNow and Jira server (an `httptest` server built from the fixtures) in the
  live e2e suite, because neither can be self-hosted in kind (the same reasoning as the contract
  rows for Bitbucket and Azure DevOps in `test/e2e/coverage.tsv`).

## 6. The gate

Options for how the gate waits:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | CEL: `changeRequests["prod-change"].state == "approved"`, read from the instance's status by the PolicyGate reconciler, as `metrics.*` | Composes with every other rule ("approved and outside the freeze"); explainable in the gate reason; stale reads fail closed | One more cross-CRD read (existing G8 row) |
| (b) | The step's Graph conditions require the ChangeRequest node `readyWhen` approved | Graph-native | `readyWhen` does not gate dependents in a standalone Graph (G1); kardinal's step gating is by gate instances; no reason line |
| (c) | A dedicated `spec.changeRequest` gate kind the reconciler special-cases | Short to write | A second gate model; does not compose |

**Choice: (a).** CEL variable, per gate:

| Field | Type | Value |
|-------|------|-------|
| `changeRequests.<name>.state` | string | normalised state; `""` when the instance does not exist yet |
| `changeRequests.<name>.ticket` | string | key (`CHG0031337`, `CHG-42`) |
| `changeRequests.<name>.url` | string | link |
| `changeRequests.<name>.stale` | bool | `observedAt` older than `validUntil` at evaluation |
| `changeRequests.<name>.timedOut` | bool | from status |
| `changeRequests.<name>.approvers` | list | names, when known |

```yaml
expression: 'changeRequests["prod-change"].state == "approved" && !changeRequests["prod-change"].stale'
message: "prod needs an approved change request"
```

A name with no instance for the gate's Bundle and environment is an evaluation error (fail closed),
as an unknown ChangeWindow is. The ChangeRequest watch re-evaluates the gates whose expression
contains `changeRequests` (`instanceGateRequests`). The gate reason says the ticket:
`waiting for change request CHG0031337 (Authorize / requested)`.

## 7. Polling, webhooks, limits, idempotency

### 7.1 How state reaches status

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Poll every `pollInterval` | Works behind firewalls; no inbound path; simple | Latency up to the interval; API quota |
| (b) | Webhooks only (ServiceNow Business Rule / Flow outbound REST, Jira webhooks) | Prompt | Needs inbound reachability and per-instance setup; a lost webhook leaves the gate waiting |
| (c) | Poll, plus an optional webhook that only **triggers** a re-read | Prompt when set up, correct without it | Two paths to test |

**Choice: (c)**, the Subscription pattern (`/webhook/subscriptions/...`, which only sets
`kardinal.io/refresh`): `POST /webhook/changerequests/<namespace>/<name>/<token>` authenticates the
per-ChangeRequest token (a Secret, compared in constant time), annotates the instance, and the
reconciler re-reads the ticket through the API. The webhook body is never trusted for state.

### 7.2 Rate limits

The MetricCheck `Limiter` (`pkg/reconciler/metriccheck/limiter.go`: global and per-namespace
slots, FIFO) is generalised into a shared limiter for outbound provider calls, with its own
budget for ChangeRequests (`--change-request-max-inflight`, `--change-request-max-inflight-per-namespace`).
HTTP 429 and `Retry-After` are honoured; the reconciler writes condition `ProviderError` with the
retry time and requeues then. A read failure keeps the last state but lets it go stale, so the
gate blocks once `validUntil` passes.

### 7.3 Idempotency: never two tickets

The reconciler is re-run after crashes, and a create can succeed while its response is lost.

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Record the ticket id in status after create | Simple | A lost response, or a crash between create and status write, opens a second ticket |
| (b) | Deterministic correlation id written into the ticket (`correlation_id` / a Jira label), and *search before create* on every attempt | Survives lost responses and crashes | One extra read before each create |
| (c) | (b) plus `status.pendingCreate` written before the create (an outbox), cleared after | Bounds the search to the window where it matters | More state |

**Choice: (c).** The correlation id is `kardinal:<namespace>/<bundle>/<environment>/<bundle UID>`
(the UID, so a re-created Bundle of the same name gets a new ticket); its SHA-256 prefix is the Jira
label. Before every create the reconciler searches by it and adopts what it finds. The status write
of `pendingCreate` uses the optimistic lock (`MergeFromWithOptimisticLock`), as the audit outbox
does, so two reconciles from a stale cache cannot both create.

### 7.4 Open or link

- **open**: kardinal creates the ticket (§7.3).
- **link**: kardinal never creates; it reads the key from a structured approval field (§8), a
  Bundle label or annotation, and then tracks it. A link that does not resolve is `state: unknown`
  with condition `ProviderError` (`ticket CHG-9 not found`); the gate blocks.
- Linking from an Approval field is checked: the ticket must carry the correlation id, or match a
  per-template `link.requireField` (for example the change's `cmdb_ci` equals a configured value),
  so an approver cannot paste an unrelated approved ticket. Without either check the link is
  marked `verified: false` in status and the receipt.

## 8. Structured approval fields

### 8.1 Shape

The gate declares what an approval must carry; the Approval carries it.

```yaml
# PolicyGate
spec:
  approval:
    required: 1
    excludeAuthor: true
    fields:
      - name: ticketID
        type: string            # string | bool | int | url | enum
        required: true
        pattern: '^CHG[0-9]{7}$'
        description: "ServiceNow change number"
      - name: riskAcknowledged
        type: bool
        required: true
        mustBe: true            # bool: the only accepted value
        description: "I have read the migration notes"
      - name: rollbackPlan
        type: url
---
# Approval
spec:
  decision: approve
  fields:                       # map[string]string, at most 16 entries, values ≤ 1024 bytes
    ticketID: CHG0031337
    riskAcknowledged: "true"
```

At most 16 declared fields per gate; names are CEL identifiers (`^[a-zA-Z][a-zA-Z0-9_]{0,62}$`).

### 8.2 Where values are validated

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | CRD CEL on the Approval | Refused at write | The Approval's schema cannot see the gate's declaration |
| (b) | A ValidatingAdmissionPolicy with the gate as params | Refused at write | A binding's `paramRef` is static, not per request (17-actors §5.1); the gate of a Bundle and environment cannot be looked up |
| (c) | The PolicyGate reconciler, at tally: a decision whose fields do not satisfy the declaration does not count, with the reason | Uses the gate's own spec (the Graph copies Approvals into `spec.approvals`); no lookup; reasons show in `status.approvals` | The Approval is admitted, then not counted |
| (d) | (c), plus the CLI and the UI validate against the gate before writing | People get the error before they submit | Two implementations to keep equal |

**Choice: (d)** with one implementation: `lifecycle.ApprovalFieldsValid(policy, fields)` is called
by the tally, by `kardinal approve` and by the UI API, as `ApprovalStandingOf` is today. The
Approval CRD adds only shape rules (map size, value length, key pattern). `GateApproval` gains
`fields`, so the Graph copies them; `approvalsOfItem` copies the whole spec already.

### 8.3 In CEL and in records

- `approvals.fields.<name>` is the list of values given by the **counted** approvals, so a gate can
  require more than presence: `approvals.fields.ticketID.exists(t, t == changeRequests["prod-change"].ticket)`.
- `status.approvals[].fields` records each decision's fields; `ApprovalRecorded` AuditEvents
  include them (values truncated to 256 bytes in the message, whole in a structured field);
  the receipt includes them.
- A field is a statement by the approver, verified only as theirs (the Approval's identity
  admission), not as true. `riskAcknowledged: true` is evidence that the approver said so.

### 8.4 CLI and UI

```
kardinal approve checkout prod --field ticketID=CHG0031337 --field riskAcknowledged=true
```

- `--field` is repeatable; missing required fields are asked for at a terminal (with the
  description), refused otherwise, before anything is written.
- `kardinal why` lists the fields an approval still needs.
- The UI's approval form renders one input per declared field (text, checkbox, URL, select for
  enum), validates with the same rules (from the UI API), and posts them with the decision.

### 8.5 Kargo's `get-user-input`

Kargo documents a promotion step that pauses until a person supplies inputs. We have not read its
implementation; this spec relies only on its documented purpose. The difference is placement:
here the inputs belong to an approval decision on a gate, so they carry the one-party rule,
admission-verified identity and the audit trail of approvals, and they do not add a step type.

## 9. Evidence

| Where | What |
|-------|------|
| PR evidence | A "Change request" row per environment: ticket key linked to its URL, state, and whether a link was verified |
| `kardinal why` / UI stage drawer | The ticket and its state while the gate waits |
| AuditEvents | `ChangeRequestOpened`, `ChangeRequestLinked`, `ChangeRequestApproved`, `ChangeRequestRejected` (new actions), from the ChangeRequest outbox |
| Receipt (`21-receipts.md`) | The ChangeRequest status snapshot (key, URL, state, approvers, verified) and the approval fields, as gate inputs |
| NotificationHooks | Unchanged; a ChangeRequest does not notify |

## 10. What this does not protect against

- **The ticketing system's own controls.** kardinal reports what the API says; who may approve a
  change there is the ticketing system's business.
- **A compromised ticketing credential** can mark tickets approved. Use a credential that may
  create and read, not approve.
- **Unverified links.** In `link` mode without a correlation id or `requireField` check, an
  approver can link any approved ticket; status and receipt say `verified: false`.
- **Field truth.** Structured fields are statements, not facts (§8.3).

## 11. Upgrade

New CRD (`changerequests`), new optional fields (`approval.fields`, `ApprovalSpec.fields`,
`GateApproval.fields`, `status.approvals[].fields`), new AuditEvent actions. Nothing existing
changes behaviour. Graphs built before the upgrade have no ChangeRequest nodes.

## 12. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| CR1 | Where the ticket lives | PolicyGate fields; ChangeRequest CR; MetricCheck provider | **ChangeRequest CR** (3): the MetricCheck per-promotion pattern, I/O isolated, status is evidence |
| CR2 | How the gate waits | CEL over status; `readyWhen`; special gate kind | **CEL** (6): composes, explains, fails closed on stale reads |
| CR3 | State updates | poll; webhooks; poll + webhook trigger | **Poll + trigger** (7.1): correct without inbound access, prompt with it; body never trusted |
| CR4 | No duplicate tickets | id in status; correlation id + search; + create outbox | **Correlation id + search + outbox** (7.3) |
| CR5 | Link verification | trust; correlation id; configured field match | **Correlation id or `requireField`, else `verified: false`** (7.4) |
| CR6 | Field validation | CRD CEL; VAP params; reconciler; reconciler + clients | **Reconciler + clients, one function** (8.2) |
| CR7 | Fields in CEL | none; per decision; values of counted approvals | **Values of counted approvals** (8.3): only votes that count are policy inputs |

## 13. Test plan

- **Unit**: state mapping tables (ServiceNow, Jira, custom maps); correlation id and search-before-
  create, including a lost create response (the fake returns 500 after creating) and two concurrent
  reconciles; limiter and 429/`Retry-After`; egress refusal; stale reads; field validation for each
  type, `pattern`, `mustBe`, size limits; `approvals.fields` from counted approvals only.
- **Fake-client journeys**: open → pending → approved → prod promotes; rejected blocks; link from an
  approval field, verified and unverified; a missing required field does not count and `why`
  names it; re-run after a crash between create and status write opens one ticket.
- **Live e2e** (kind, COUNT=2, coverage rows): against the fixture-built fake ServiceNow and Jira
  servers in the cluster: the gate waits and opens, the webhook trigger re-reads, the PR evidence
  row; `kardinal approve --field` end to end and the UI API form.
- **Contract**: the recorded fixtures from a ServiceNow PDI and a Jira Cloud site (5.4).
