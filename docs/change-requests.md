# Change Requests

Many teams may not change production without an approved change request: a ticket in ServiceNow
or Jira that a change advisory board (CAB) or a release manager approves. A `ChangeRequest` lets
kardinal open that ticket for a promotion, or track one someone already opened, and lets a
[gate](policy-gates.md) wait until it is approved:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: cab-approval
  namespace: checkout
  labels:
    kardinal.io/scope: team
    kardinal.io/applies-to: prod
spec:
  expression: 'changeRequests["prod-change"].state == "approved" && !changeRequests["prod-change"].stale'
  message: "prod needs an approved change request"
  recheckInterval: 1m
```

!!! note "Providers"
    This release serves ServiceNow. Jira comes next: until it is served, a `provider: jira`
    instance reports the condition `ProviderError` (`provider "jira" is not available in this
    controller`) and a gate that reads it blocks.

## How it works

A `ChangeRequest` with `spec.perPromotion: true` is a template, like a
[per-promotion MetricCheck](metric-checks.md#per-promotion-analysis):

1. Each Bundle's Graph creates an **instance** of the template for every environment whose gates
   read `changeRequests.<name>` and that the template applies to (`spec.appliesTo`). The instance
   is created once every upstream environment is Verified, so no ticket is opened for a version
   that never reached the gate.
2. The ChangeRequest controller opens the ticket (`mode: open`) or reads its key from the
   promotion (`mode: link`), then reads the ticket's state every `pollInterval` and writes it to
   the instance's status.
3. The gate reads the instance's status as `changeRequests.<template name>`. It never calls the
   ticketing system itself.
4. When the Bundle is Verified or Failed the Graph sets `spec.suspend` on the instance and the
   controller stops reading the ticket. A Superseded Bundle's Graph holds its steps, so its
   instances are not suspended; they are deleted with the Graph when it is retired
   (`graph.retire.superseded`, one minute by default). kardinal does not close or cancel the ticket.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeRequest
metadata:
  name: prod-change            # gates read changeRequests["prod-change"]
  namespace: checkout          # the Pipeline namespace
spec:
  perPromotion: true
  appliesTo: [prod]            # unset: every environment whose gates read it
  provider: servicenow         # servicenow | jira
  mode: open                   # open (default) | link
  servicenow:
    instanceURL: https://example.service-now.com
    credentialsSecretRef: {name: snow-kardinal, key: token}
    scheme: basic              # basic (user:password) | bearer (OAuth token)
    type: normal               # normal | standard | emergency
    template:
      short_description: "Promote {{ pipeline.name }} {{ bundle.version }} to {{ environment.name }}"
      assignment_group: payments-cab
  pollInterval: 1m             # default 1m, minimum 30s
  timeout: 24h                 # status.timedOut after it; "0" for none
```

The instance is named `<template>-<environment>--<bundle>` (hash-shortened when too long) and
labelled `kardinal.io/change-request-template`, `kardinal.io/bundle`, `kardinal.io/environment`
and `kardinal.io/bundle-uid`:

```bash
kubectl get changerequests -n checkout
# NAME                          PROVIDER     TICKET       STATE      AGE
# prod-change                   servicenow                           3d
# prod-change-prod--checkout-7f3c2a   servicenow   CHG0031337   approved   12m
```

### Ticket templates

`template` holds the fields the ticket is created with, as strings. The placeholders are the
per-promotion MetricCheck ones: `{{ bundle.name }}`, `{{ bundle.version }}`, `{{ bundle.imageTag }}`,
`{{ bundle.imageDigest }}`, `{{ bundle.commitSHA }}`, `{{ pipeline.name }}`,
`{{ environment.name }}` and `{{ namespace }}`. A value is put into the field as text: control
characters are dropped and it is cut to 256 bytes. An unknown placeholder is left as it is.

### Credentials

`credentialsSecretRef` names a Secret key in the ChangeRequest's namespace. The Secret must be
labelled `kardinal.io/referenceable: "true"`, as for MetricChecks and NotificationHooks: its owner
agrees to have it sent to the address whoever writes the ChangeRequest chooses. Give kardinal a
credential that may create and read changes, not one that may approve them. Every request goes
through the controller's egress guard: loopback, link-local and cloud metadata addresses are
refused (condition `Refused`), and with `--egress-allowlist` only the listed destinations are
reached.

## ServiceNow

kardinal uses the Table API: it creates and reads `change_request` records, and reads their
approvals from `sysapproval_approver`.

| Call | Request |
|---|---|
| Open | `POST /api/now/table/change_request` with the template's fields, `type` (`spec.servicenow.type`), `correlation_id` and `correlation_display: kardinal` |
| Find (before every create) | `GET /api/now/table/change_request?sysparm_query=correlation_id=<key>^ORDERBYsys_created_on` |
| Read | `GET /api/now/table/change_request/<sys_id>`, or by `number` for a linked ticket |
| Approvers | `GET /api/now/table/sysapproval_approver?sysparm_query=sysapproval=<sys_id>` (a refusal leaves them out) |

The template cannot set `type`, `correlation_id` or `correlation_display`. `correlation_id`
holds `kardinal-` and 32 hex characters of the promotion's correlation id's SHA-256, because the
field holds 100 characters. Give the integration user a role that may create and read
`change_request` and read `sysapproval_approver` (for example `itil`), and no right to approve.

The state is read from `state` and `approval`, mapped with the default Change Management state
model:

| ServiceNow | `state` |
|---|---|
| `approval` is `rejected` | `rejected`, always: no `states` map overrides it |
| `state` Canceled (`4`) | `cancelled` |
| `state` Closed (`3`) or Review (`0`) | `closed` |
| `approval` is `approved` and `state` Scheduled (`-2`) or Implement (`-1`) | `approved` |
| anything else (New, Assess, Authorize, approval requested) | `pending` |

An instance with its own state model maps its values in `spec.servicenow.states`, by raw value or
display name, before the defaults (and after a rejected `approval`, which always wins). A value
mapped to `approved` counts only when `approval` is `approved`; otherwise it reads `pending`, so a
map can never approve what nobody approved:

```yaml
servicenow:
  states:
    "-3": approved        # this instance's approved changes wait at Authorize
    "Implement": closed
```

For a linked ticket, `link.requireField` names a `change_request` field. A reference field such as
`cmdb_ci` matches by its sys_id or its display value. `providerState` is the state and approval
as ServiceNow displays them (`Scheduled / Approved`).

**An instance behind an internal CA.** Put the CA certificates (PEM) in a ConfigMap in the release
namespace and set `changeRequest.caBundle.configMap` (and `.key`, default `ca.crt`). The controller
then trusts them, besides the system roots, for ChangeRequest calls only
(`--change-request-ca-file`).

## The gate

`changeRequests.<name>` has `state`, `ticket`, `url`, `stale`, `timedOut`, `verified`,
`approvers` and `providerState` ([CEL context](reference/cel-context.md#changerequests)).

| `state` | Meaning |
|---|---|
| `""` | No ticket yet: the instance does not exist (its upstreams are not Verified), or has not opened or found its ticket |
| `pending` | The ticket waits for approval |
| `approved` | Approved, and not yet implemented or closed |
| `rejected` | Rejected |
| `cancelled` | Cancelled |
| `closed` | Closed or in review |
| `unknown` | The provider's state maps to none of these, the linked ticket was not found, or the last reading is stale |

A reading is fresh for two poll intervals. After that, `stale` is true and `state` reads
`unknown`, so `state == "approved"` alone already blocks when the ticketing system cannot be
reached. A name with no template is an evaluation error, and the gate blocks. While it blocks,
the gate's reason names the ticket:

```text
expression false; change request prod-change: ticket CHG0031337 is pending (Authorize / requested)
```

Org gates (templates in an org policy namespace) read the org namespace, where the Graph creates
no instances: their `changeRequests` entries never have a ticket, so they block.

## Open or link

- **open** (default): kardinal opens the ticket. It writes the promotion's correlation id
  (`kardinal:<namespace>/<bundle>/<environment>/<bundle UID>`) into the ticket, and before every
  create it searches for a ticket with that id and adopts it. The create is first recorded in
  `status.pendingCreate`, so a controller restart or a lost response never opens a second
  ticket. A Bundle deleted and created again under the same name has a new UID, so it gets a new
  ticket.
- **link**: kardinal never opens a ticket. It reads the key from the promotion and tracks that
  ticket:

```yaml
spec:
  mode: link
  link:
    from: approvalField        # approvalField | bundleLabel | bundleAnnotation
    name: ticketID             # the approval field, label or annotation
    requireField:              # optional: the ticket's field must have this value
      name: cmdb_ci
      value: checkout
```

`approvalField` reads a [structured approval field](approvals.md#approval-fields) of the
environment's counted approvals: `kardinal approve checkout prod --field ticketID=CHG0031337`.
Approvals that do not count give no key, and two counted approvals with different keys are
refused (condition `ProviderError`). `bundleLabel` and `bundleAnnotation` read the Bundle CI
created.

A linked ticket is **verified** when it carries the promotion's correlation id, or when all of
these hold:

- its `link.requireField` field has that value;
- it was created after the Bundle: an older ticket, approved for an earlier version, cannot be this
  version's;
- no other ChangeRequest holds it from earlier: one that opened it, or one that linked it first
  (`status.linkedAt`). One ticket approves one promotion.

Holders are counted across the cluster: a team that links a ticket key first, from its own
approval fields, leaves another team's later link of the same key unverified. That fails closed
(it can delay a promotion, never approve one); use `requireField` values that belong to your
service, and prefer mode `open`, whose tickets carry the promotion's correlation id.

Otherwise `status.verified` and `changeRequests.<name>.verified` are false, `verifiedReason` says
which rule failed, and the `ChangeRequestLinked` record says so: an approver could have linked an
unrelated, old or already used approved ticket. Require it in the gate:

```yaml
expression: 'changeRequests["prod-change"].state == "approved" && changeRequests["prod-change"].verified'
```

## Status and records

| Field | Meaning |
|---|---|
| `status.ticket` | `key`, `id` (sys_id or issue id) and `url` |
| `status.state`, `status.providerState` | The normalised and the provider's own state |
| `status.approvers` | The ticket's approvers, when the provider reports them |
| `status.observedAt`, `status.validUntil` | The last reading, and when it goes stale (two poll intervals later) |
| `status.timedOut` | `spec.timeout` has passed since the instance started |
| `status.retryAt` | After an error, when the next request is sent |
| `status.conditions` | `Ready`, `Stale`, `CredentialsInvalid`, `Refused` (egress), `ProviderError` |

The instance writes AuditEvents `ChangeRequestOpened`, `ChangeRequestLinked`, and, the first time
the ticket is read so, `ChangeRequestApproved` and `ChangeRequestRejected`
(`kardinal get auditevents --pipeline checkout --env prod`, or
`kubectl get auditevents -l kardinal.io/action=ChangeRequestApproved`).

Only kardinal writes an instance: the chart's graph-objects admission policy refuses anyone else
(a user with full RBAC, the cluster admin) creating one or changing its status. A template has no
`kardinal.io/bundle` label and is yours to write.

## Limits and errors

- **Rate.** An instance makes at most two requests per poll interval (a search and a create, or a
  read and its approvers). At most `changeRequest.maxInflight` requests run at once in the cluster and
  `changeRequest.maxInflightPerNamespace` per namespace (controller flags
  `--change-request-max-inflight`, `--change-request-max-inflight-per-namespace`; defaults 8 and
  1); the rest wait, first come, first served.
- **Errors.** A failed request keeps the last reading, which goes stale, and sets
  `status.retryAt`: one poll interval later, the provider's `Retry-After` when longer (HTTP 429),
  and at least five minutes after a refused credential (`CredentialsInvalid`) or an egress refusal
  (`Refused`), so a revoked token causes no request storm.
- **Timeout.** `status.timedOut` is information for the gate's expression
  (`!changeRequests["prod-change"].timedOut`); kardinal does not cancel the ticket.
