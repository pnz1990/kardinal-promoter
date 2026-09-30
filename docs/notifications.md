# Notifications

kardinal-promoter can deliver outbound webhooks when promotion events occur.
This allows platform teams to integrate with Slack, PagerDuty, custom alerting systems,
or any HTTP endpoint.

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
    # HTTPS URL to POST the notification payload to (see §Slack example).
    url: https://hooks.slack.com/triggers/T.../...
    # Optional Authorization header value (for Bearer token auth).
    # Stored in plain text in the spec; see §Authorization.
    # authorizationHeader: "Bearer my-secret-token"
  events:
    - Bundle.Verified       # bundle promoted successfully through all environments
    - Bundle.Failed         # bundle failed during promotion
    - PolicyGate.Blocked    # a policy gate blocked a promotion
    - PromotionStep.Failed  # a specific promotion step failed
  # Optional: restrict notifications to a specific pipeline by name.
  # pipelineSelector: nginx-demo
```

Apply it with `kubectl apply -f my-hook.yaml`.

---

## Webhook payload

The controller POSTs a JSON body to the configured URL on each qualifying event:

```json
{
  "event":       "Bundle.Verified",
  "pipeline":    "nginx-demo",
  "bundle":      "nginx-demo-abc123",
  "environment": "prod",
  "message":     "Bundle nginx-demo-abc123 is Verified",
  "timestamp":   "2026-04-21T10:00:00Z"
}
```

| Field         | Type   | Description                                          |
|---------------|--------|------------------------------------------------------|
| `event`       | string | Event type (see §Events)                             |
| `pipeline`    | string | Pipeline name                                        |
| `bundle`      | string | Bundle name                                          |
| `environment` | string | Environment name (PolicyGate and PromotionStep events) |
| `message`     | string | Human-readable description                          |
| `timestamp`   | string | RFC3339 UTC timestamp of delivery                    |

---

## Events

| Event type                | When it fires                                                   |
|---------------------------|-----------------------------------------------------------------|
| `Bundle.Verified`         | A Bundle reaches Phase=Verified (all environments succeeded)    |
| `Bundle.Failed`           | A Bundle reaches Phase=Failed                                   |
| `PolicyGate.Blocked`      | A PolicyGate instance starts blocking a Bundle (once per block) |
| `PromotionStep.Failed`    | A PromotionStep transitions to state=Failed                     |

`PolicyGate.Blocked` is sent once each time a gate instance goes from not
evaluated or allowed to blocked. Re-evaluations that keep blocking do not send
it again; a gate that allows and later blocks again sends it again. The block is
identified by the gate's `Ready` condition, whose `lastTransitionTime` only
moves when the gate flips. Gate templates (PolicyGates without a
`kardinal.io/bundle` label) are only syntax-checked, never evaluated, so they
never send `PolicyGate.Blocked`.

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
  with exponential backoff: 30s, 1m, 2m, 4m, 8m, then every 10m. After 10
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
  Cluster Services and other private addresses are allowed. See
  [Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls).

---

## Slack example

```yaml
apiVersion: kardinal.io/v1alpha1
kind: NotificationHook
metadata:
  name: slack-prod-alerts
  namespace: default
spec:
  webhook:
    url: https://hooks.slack.com/triggers/T.../...   # Workflow Builder webhook trigger
  events:
    - Bundle.Failed
    - PolicyGate.Blocked
    - PromotionStep.Failed
  pipelineSelector: nginx-prod  # only prod pipeline failures
```

The payload has no `text` or `blocks` key, so a classic Slack incoming webhook
(`https://hooks.slack.com/services/...`) rejects it with HTTP 400 and the delivery is
retried and then given up on (§Delivery). Use a Slack Workflow Builder webhook trigger
instead (`https://hooks.slack.com/triggers/...`): declare `event`, `pipeline`, `bundle`
and `message` as its variables and post them from a workflow step. Any relay that reshapes
the JSON works too.

---

## Authorization

To add a Bearer token to the webhook POST:

```yaml
spec:
  webhook:
    url: https://alerting.example.com/kardinal-events
    authorizationHeader: "Bearer my-secret-token"
```

The value is sent exactly as written. There is no variable expansion: a value
such as `"Bearer ${ALERT_TOKEN}"` is sent literally. The value is stored in
plain text in the NotificationHook spec, so anyone who can read the hook can
read the token. Restrict `get` and `list` on `notificationhooks` accordingly.
A Secret-backed header is not available yet.

Incoming-webhook URLs (Slack, Microsoft Teams) carry their token in the path.
The controller never writes the URL to status or logs; logs show only the host.

---

## Status

The NotificationHook status shows the last successful delivery and the
delivery state:

```bash
kubectl get notificationhook my-slack-hook -o yaml
```

```yaml
status:
  lastSentAt: "2026-04-21T10:05:00Z"
  lastEvent: "Bundle.Verified"
  lastEventKey: "Bundle.Verified/nginx-demo-abc123"
  processedEventKeys:
    - "Bundle.Verified/nginx-demo-abc123"
    - "PolicyGate.Blocked/nginx-demo-abc123-prod-no-weekend-deploys/2026-04-18T10:00:00Z"
  observedGeneration: 1
  failedAttempts: 0   # consecutive failed attempts for the current event
  failureMessage: ""  # cleared on success
```

`failureMessage` is set when the webhook returns a non-2xx status or the
connection fails, for example
`delivery of Bundle.Failed/nginx-demo-abc123 failed (attempt 2 of 10): webhook returned HTTP 503`.
See §Delivery for the retry schedule.

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
