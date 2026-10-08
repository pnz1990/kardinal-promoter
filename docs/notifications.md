# Notifications

kardinal-promoter can deliver outbound webhooks when promotion events occur.
The body is the kardinal JSON payload, a Slack message, a Microsoft Teams Adaptive Card, or
a body you write yourself as a template (see [Formats](#formats)), so Slack, Teams,
PagerDuty, Opsgenie or any HTTP endpoint can receive events without a relay.

---

## NotificationHook CRD

A `NotificationHook` defines a webhook endpoint and the event types that trigger delivery.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: NotificationHook
metadata:
  name: my-slack-hook
  namespace: default
spec:
  webhook:
    # The Secret holds the URL (key url) and/or the Authorization header
    # (key authorization); see §Authorization. Use https://. http:// is also
    # accepted and sends the payload and any Authorization header unencrypted.
    secretRef:
      name: slack-webhook
    # Or, for a URL without a token in it:
    # url: https://alerting.example.com/kardinal-events
  format: slack             # json (default), slack, teams or template
  events:
    - Bundle.Verified       # bundle promoted successfully through all environments
    - Bundle.Failed         # bundle failed during promotion
    - PolicyGate.Blocked    # a policy gate blocked a promotion
    - PromotionStep.Failed  # a specific promotion step failed
  # Optional: restrict notifications to a specific pipeline by name.
  # pipelineSelector: nginx-demo
```

```bash
kubectl create secret generic slack-webhook -n default \
  --from-literal=url=https://hooks.slack.com/services/T000/B000/XXXX
```

Apply it with `kubectl apply -f my-hook.yaml`.

---

## Webhook payload

The controller POSTs a JSON body to the configured URL on each qualifying event:

```json
{
  "event":     "Bundle.Verified",
  "pipeline":  "nginx-demo",
  "bundle":    "nginx-demo-abc123",
  "message":   "Bundle nginx-demo-abc123 is Verified",
  "timestamp": "2026-04-21T10:00:00Z"
}
```

Bundle events have no `environment` field. PolicyGate and PromotionStep events
carry the environment:

```json
{
  "event":       "PromotionStep.Failed",
  "pipeline":    "nginx-demo",
  "bundle":      "nginx-demo-abc123",
  "environment": "prod",
  "message":     "PromotionStep nginx-demo-nginx-demo-abc123-prod failed: health alarm via argocd (onHealthFailure=none): health check timeout after 10m0s; last result: waiting for argocd: health=Progressing, sync=Synced, opPhase=Succeeded",
  "timestamp":   "2026-04-21T10:12:00Z"
}
```

A PromotionStep is named `<pipeline>-<bundle>-<environment>`, and the message
after `failed: ` is the step's `status.message`.

| Field         | Type   | Description                                          |
|---------------|--------|------------------------------------------------------|
| `event`       | string | Event type (see §Events)                             |
| `pipeline`    | string | Pipeline name                                        |
| `bundle`      | string | Bundle name                                          |
| `environment` | string | Environment name (PolicyGate and PromotionStep events) |
| `message`     | string | Human-readable description                          |
| `timestamp`   | string | RFC3339 UTC timestamp of delivery                    |
| `prURL`       | string | Pull request URL (`PromotionStep.PROpened` and `PromotionStep.WaitingForApproval` only) |

Rollback events (`Bundle.RollbackStarted`, `Bundle.RolledBack`) carry the environment the
rollback targets. Every request also has the headers `X-Kardinal-Event` (the event type) and
`X-Kardinal-Event-Key` (the event key, see [Delivery guarantees](#delivery-guarantees)),
whatever the format.

---

## Events

| Event type                         | When it fires                                                   | Key                                   |
|------------------------------------|-----------------------------------------------------------------|---------------------------------------|
| `Bundle.Verified`                  | A Bundle reaches Phase=Verified (all environments succeeded)    | `Bundle.Verified/<bundle>`            |
| `Bundle.Failed`                    | A Bundle reaches Phase=Failed                                   | `Bundle.Failed/<bundle>`              |
| `Bundle.Superseded`                | A newer Bundle of the same Pipeline and type supersedes a Bundle (Phase=Superseded) | `Bundle.Superseded/<bundle>` |
| `Bundle.RollbackStarted`           | A rollback Bundle (`kardinal rollback`, the UI, `onHealthFailure: rollback`, RollbackPolicy) starts promoting | `Bundle.RollbackStarted/<bundle>` |
| `Bundle.RolledBack`                | A rollback Bundle reaches Phase=Verified: the environment runs the restored artifacts | `Bundle.RolledBack/<bundle>` |
| `PolicyGate.Blocked`               | A PolicyGate instance starts blocking a Bundle (once per block) | `PolicyGate.Blocked/<gate>/<since>`   |
| `PolicyGate.Unblocked`             | A PolicyGate instance that was blocking allows again (once per block) | `PolicyGate.Unblocked/<gate>/<since>` |
| `PromotionStep.Failed`             | A PromotionStep transitions to state=Failed                     | `PromotionStep.Failed/<step>`         |
| `PromotionStep.PROpened`           | A PromotionStep opens its promotion pull request (`status.prURL` is set) | `PromotionStep.PROpened/<step>` |
| `PromotionStep.WaitingForApproval` | A PromotionStep waits for its PR to be reviewed and merged (state=WaitingForMerge, `approval: pr-review`) | `PromotionStep.WaitingForApproval/<step>` |

`<since>` is the RFC3339 time the gate's `Ready` condition last changed. Only `pr-review`
environments open promotion PRs today, so `PromotionStep.PROpened` and
`PromotionStep.WaitingForApproval` come together: subscribe to `PROpened` to log every PR
(rollback PRs included) and to `WaitingForApproval` to page a reviewer.
`Bundle.Verified` and `Bundle.Failed` also fire for rollback Bundles.

Steps stopped by `onHealthFailure: abort` or `rollback` end in `AbortedByAlarm` or
`RollingBack` and do not send `PromotionStep.Failed`. An `AbortedByAlarm` step fails its Bundle,
so `Bundle.Failed` catches it. With `rollback`, the rollback Bundle supersedes the failing one
(see [Rollback](rollback.md#automatic-rollback)), and the rollback Bundle's own `Bundle.Verified`
or `Bundle.Failed` reports the outcome.

`PolicyGate.Blocked` is sent once each time a gate instance goes from not
evaluated or allowed to blocked. Re-evaluations that keep blocking do not send
it again; a gate that allows and later blocks again sends it again. The block is
identified by the gate's `Ready` condition, whose `lastTransitionTime` only
moves when the gate flips. `PolicyGate.Unblocked` is sent once each time a
blocking gate instance allows again: its `Ready` condition is then `True` with
reason `Unblocked` (a gate that allows on its first evaluation has reason
`Allowed` and sends nothing). Gate templates (PolicyGates without a
`kardinal.io/bundle` label) are only syntax-checked, never evaluated, so they
send neither.

---

## Formats

`spec.format` picks the body. Every format is POSTed with the same retries and headers.

| `format` | Body | Content-Type |
|----------|------|--------------|
| `json` (default) | The kardinal payload ([Webhook payload](#webhook-payload)) | `application/json` |
| `slack` | A Slack incoming-webhook message: fallback `text` and Block Kit `blocks` | `application/json` |
| `teams` | A Microsoft Teams Workflows webhook message with an Adaptive Card 1.4 attachment | `application/json` |
| `template` | `spec.template.body` rendered over the event ([Templated body](#templated-body)) | `spec.template.contentType`, default `application/json` |

### Slack

Create an [incoming webhook](https://api.slack.com/messaging/webhooks) for the channel and
put its URL in a Secret (the URL is the credential):

```yaml
apiVersion: kardinal.io/v1alpha1
kind: NotificationHook
metadata:
  name: slack-prod-alerts
  namespace: default
spec:
  webhook:
    secretRef:
      name: slack-webhook          # key url: https://hooks.slack.com/services/T.../B.../...
  format: slack
  events:
    - Bundle.Failed
    - PolicyGate.Blocked
    - PromotionStep.WaitingForApproval
  pipelineSelector: nginx-prod
```

The message has a header with the event title, the event message (Slack's `&`, `<` and `>`
escaped), the pipeline, Bundle and environment as fields, an **Open pull request** button
when the event has a PR, and a context line with the event key and time. `text` carries
`<title>: <message>` for notifications and clients that do not show blocks. Texts are cut
to Slack's block limits. A `json` hook pointed at a Slack incoming webhook gets HTTP 400
`no_text` and is retried, then given up on.

### Microsoft Teams

In Teams, add the **Workflows** app's "Post to a channel when a webhook request is received"
flow to the channel and put its URL in a Secret:

```yaml
spec:
  webhook:
    secretRef:
      name: teams-webhook          # key url: https://prod-00.westeurope.logic.azure.com:443/workflows/...
  format: teams
  events: [Bundle.Verified, Bundle.Failed, Bundle.RolledBack]
```

The body is a `message` whose one attachment is an Adaptive Card (version 1.4, full width):
the event title (colored by outcome), the message, a fact set with the pipeline, Bundle and
environment, an **Open pull request** action when the event has a PR, and the event key and
time. Office 365 connector URLs (`webhook.office.com`), which Microsoft retired, are not
supported.

### Templated body

`format: template` renders `spec.template.body`, a Go
[text/template](https://pkg.go.dev/text/template), over this event:

| Field | Description |
|-------|-------------|
| `.Event` | Event type, e.g. `Bundle.Verified` |
| `.Key` | Event key ([Events](#events)); the same event always has the same key |
| `.Pipeline`, `.Bundle`, `.Environment` | Where it happened; empty when the event has none |
| `.Message` | The `message` of the json payload |
| `.Timestamp` | RFC3339 UTC delivery time |
| `.PRURL` | Pull request URL, or empty |
| `.Hook`, `.Namespace` | The NotificationHook's name and namespace |

An [Opsgenie](https://docs.opsgenie.com/docs/alert-api) alert, with the API key in the
Secret (`authorization: GenieKey <key>`) and the event key as the alias, so Opsgenie drops a
repeated delivery:

```yaml
spec:
  webhook:
    url: https://api.opsgenie.com/v2/alerts
    secretRef:
      name: opsgenie               # key authorization: "GenieKey 0000-..."
  format: template
  template:
    body: |
      {
        "message": {{ .Message | truncate 130 | json }},
        "alias": {{ json .Key }},
        "source": {{ printf "kardinal/%s/%s" .Namespace .Pipeline | json }},
        "priority": {{ if eq .Event "Bundle.Failed" "PromotionStep.Failed" }}"P2"{{ else }}"P4"{{ end }},
        "details": {"bundle": {{ json .Bundle }}, "environment": {{ json .Environment }}, "pr": {{ json .PRURL }}}
      }
  events: [Bundle.Failed, PromotionStep.Failed, PromotionStep.WaitingForApproval]
```

Functions, on top of the text/template builtins (`if`, `with`, `eq`, `and`, `index`,
`len`, ...): `json` (the value as JSON, quotes and escapes included: use it for every
string in a JSON body), `lower`, `upper`, `truncate N` (at most N characters, with `…` when
cut) and `printf` (widths and precisions up to 999, no `*`).

Limits, so a template cannot run away with the controller:

- `range`, `define`, `block` and `template` are refused: rendering is linear in the
  template's size. The body is at most 16 KiB, the rendered body at most 64 KiB.
- A field that does not exist is an error.
- With a JSON content type (`application/json` or `*+json`), the rendered body must be
  valid JSON.

A template that does not parse, or uses a refused action, makes the hook `Ready=False`
(reason `InvalidTemplate`) and nothing is sent until it is fixed. A template that parses but
fails to render for one event (invalid JSON, a body over 64 KiB) cannot succeed on a retry,
so that event is given up on at once (`failureMessage: gave up on <key>: template: ...`) and
the next event is delivered.

---

## Delivery

- Every qualifying event is delivered once, oldest first. The controller
  records the key of each delivered event in `status.processedEventKeys`, so a
  gate that stays blocked does not hide a later `Bundle.Failed`. Only the 100
  newest qualifying events are tracked.
- If the controller restarts between a POST and the status write that records
  it, that one event is sent again.
- When a hook is created, only the newest event that already exists is
  delivered; older ones are recorded as processed and not backfilled.
- One reconcile sends at most 10 events; the rest follow straight away.
- A failed delivery (non-2xx response, connection error, timeout) is retried
  with exponential backoff: 30s, 1m, 2m, 4m, 8m, then every 10m. The time of
  the next attempt is in `status.nextRetryAt`; no POST is made before it,
  whatever else changes in the namespace. After 10
  failed attempts the controller gives up on that event, records it in
  `failureMessage`, and moves on. Later events wait until the failing one
  succeeds or is given up on. Editing the hook's spec (for example fixing the
  URL) retries at once.
- The controller does not follow redirects. A 3xx response is a failed
  delivery.
- Requests time out after 10 seconds.
- The controller refuses to connect to loopback, link-local (cloud metadata),
  unspecified and multicast addresses, checked on the resolved address. Such a
  delivery fails with `destination address is not allowed` in `failureMessage`.
  Cluster Services and other private addresses are allowed. An operator can
  narrow this to a list of hosts and CIDRs with the controller's egress
  allowlist (chart value `egress.allowlist`); a URL outside it fails with
  `not in the controller egress allowlist`. See
  [Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls).

### Delivery guarantees

Events are derived from the state of Bundles, PolicyGates and PromotionSteps, each with a
deterministic key ([Events](#events)). The hook's `status.processedEventKeys` records the
key of every event delivered or given up on; it lives in the API server, not in the
controller's memory.

What is guaranteed:

- **At least once.** An event that still qualifies is retried until a 2xx response, or
  until 10 attempts fail (then it is given up on and named in `failureMessage`).
- **No duplicate after a restart, an upgrade or a leader change.** The key is written to
  status right after each successful POST, before the next one, and the new controller
  reads it from there. The one exception: the controller stops after the receiver
  answered 2xx and before that status write. Then that one event (never more) is sent
  again. Receivers that must not act twice can drop a request whose `X-Kardinal-Event-Key`
  they have seen; the same key is in the body of the `slack` and `teams` formats and is
  `.Key` in a template.
- **Order.** Events are sent oldest first, one at a time; a failing event holds the later
  ones back until it succeeds or is given up on.

What is not guaranteed:

- **Short-lived states.** An event exists while its state holds. A gate that blocks and
  allows again before the controller sees the block, or a Bundle deleted before its event
  is sent, sends nothing. Steps, Bundles and gate flips normally last far longer than a
  reconcile (milliseconds after the watch event).
- **History.** A new hook sends only the newest event that already exists. Only the 100
  newest qualifying events are tracked per hook.
- **A broken hook.** While the hook is `Ready=False` (a missing Secret, a bad URL or
  template), events wait and none is sent; they are delivered once it is fixed, if they
  still qualify.

---

## Authorization

Keep credentials in a Secret in the hook's namespace and name it in `spec.webhook.secretRef`:

```bash
kubectl create secret generic alerting-webhook -n default \
  --from-literal=authorization='Bearer my-secret-token' \
  --from-literal=url=https://alerting.example.com/kardinal-events
```

```yaml
spec:
  webhook:
    secretRef:
      name: alerting-webhook
```

| Key | Used as |
|-----|---------|
| `authorization` | The `Authorization` header, sent exactly as stored (no variable expansion) |
| `url` | The webhook URL. Takes precedence over `spec.webhook.url`. Use it for incoming-webhook URLs (Slack, Teams), which carry their token in the path |

At least one key must be present; leading and trailing whitespace (a trailing newline from
a file) is removed. The Secret is read on every delivery, so a rotated value is used for the
next request without touching the hook. Only the controller reads it (`get` on Secrets):
users who can read the hook cannot read the token. A missing Secret or key makes the hook
`Ready=False` with reason `SecretNotFound`, `SecretKeyMissing` or `URLMissing`; events wait
(no attempt is counted) and the hook is checked again every 30 seconds.

`spec.webhook.authorizationHeader` is **deprecated**. It still works: the value is sent as
the `Authorization` header exactly as written. But it is stored in plain text in the spec,
so anyone who can read the hook can read it, and the hook has the condition
`PlaintextCredential=True` (reason `AuthorizationHeaderInSpec`) while it is set. Move the
value to a Secret's `authorization` key and set `secretRef`; the API server refuses a hook
that sets both.

The controller never writes the URL or the header to status or logs; logs show only the
host. When the URL is in `spec.webhook.url`, `kubectl get notificationhooks` shows it in its
URL column, which is one more reason to keep token URLs in the Secret.

---

## Status

The NotificationHook status shows the last successful delivery and the
delivery state:

```bash
kubectl get notificationhook my-slack-hook -o yaml
```

```yaml
status:
  conditions:
    - type: Ready               # False when the hook cannot deliver as configured
      status: "True"
      reason: Configured
    # - type: PlaintextCredential   # only while spec.webhook.authorizationHeader is set
    #   status: "True"
    #   reason: AuthorizationHeaderInSpec
  lastSentAt: "2026-04-21T10:05:00Z"
  lastEvent: "Bundle.Verified"
  lastEventKey: "Bundle.Verified/nginx-demo-abc123"
  processedEventKeys:
    - "Bundle.Verified/nginx-demo-abc123"
    - "PolicyGate.Blocked/no-weekend-deploys-platform-policies-prod--nginx-demo-abc123/2026-04-18T10:00:00Z"
  observedGeneration: 1
  failedAttempts: 0   # consecutive failed attempts for the current event
  nextRetryAt: ""     # time of the next attempt after a failure
  failureMessage: ""  # cleared on success
```

`failureMessage` is set when the webhook returns a non-2xx status or the
connection fails, for example
`delivery of Bundle.Failed/nginx-demo-abc123 failed (attempt 2 of 10): webhook returned HTTP 503`.
See §Delivery for the retry schedule.

| `Ready=False` reason | Meaning |
|----------------------|---------|
| `SecretNotFound`, `SecretUnreadable` | The Secret `secretRef` names does not exist or cannot be read |
| `SecretKeyMissing` | The Secret has neither an `authorization` nor a `url` key |
| `URLMissing` | No URL in `spec.webhook.url` or the Secret |
| `InvalidURL` | The URL is not an absolute `http://` or `https://` URL |
| `InvalidAuthorization` | The Authorization value contains a line break |
| `InvalidTemplate` | `spec.template.body` does not parse or uses a refused action |

---

## Pipeline selector

`spec.pipelineSelector` restricts notifications to events from one pipeline. It
matches a Bundle's `spec.pipeline`, a PromotionStep's `spec.pipelineName` and a
PolicyGate's `kardinal.io/pipeline` label:

```yaml
spec:
  pipelineSelector: nginx-demo  # only events from this pipeline
```

When empty, events from all Pipelines in the same namespace are delivered.
