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
    # HTTPS URL to POST the notification payload to.
    url: https://hooks.slack.com/services/T.../B.../...
    # Optional Authorization header value (for Bearer token auth).
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
| `bundle`      | string | Bundle name (for Bundle events)                      |
| `environment` | string | Environment name (for PromotionStep events)          |
| `message`     | string | Human-readable description                          |
| `timestamp`   | string | RFC3339 UTC timestamp of delivery                    |

---

## Events

| Event type                | When it fires                                                   |
|---------------------------|-----------------------------------------------------------------|
| `Bundle.Verified`         | A Bundle reaches Phase=Verified (all environments succeeded)    |
| `Bundle.Failed`           | A Bundle reaches Phase=Failed                                   |
| `PolicyGate.Blocked`      | A PolicyGate transitions to ready=false (first block only)      |
| `PromotionStep.Failed`    | A PromotionStep transitions to state=Failed                     |

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
    url: https://hooks.slack.com/services/T.../B.../...
  events:
    - Bundle.Failed
    - PolicyGate.Blocked
    - PromotionStep.Failed
  pipelineSelector: nginx-prod  # only prod pipeline failures
```

Slack incoming webhooks accept any JSON payload and display the `message` field.
To get a Slack webhook URL: Settings → Integrations → Incoming Webhooks in your Slack workspace.

---

## Authorization

To add a Bearer token to the webhook POST:

```yaml
spec:
  webhook:
    url: https://alerting.example.com/kardinal-events
    authorizationHeader: "Bearer ${ALERT_TOKEN}"
```

Store the token in a Kubernetes Secret and use `envFrom` in the controller Deployment
to expose it as an environment variable, then reference it via a Helm value or
`--set controller.extraEnv`.

---

## Status

The NotificationHook status shows the last successful delivery:

```bash
kubectl get notificationhook my-slack-hook -o yaml
```

```yaml
status:
  lastSentAt: "2026-04-21T10:05:00Z"
  lastEvent: "Bundle.Verified"
  lastEventKey: "Bundle.Verified/nginx-demo-abc123"
  failureMessage: ""  # cleared on success
```

`failureMessage` is set when the webhook returns a non-2xx status or the connection fails.
The controller retries on the next reconcile triggered by a new event.

---

## Pipeline selector

`spec.pipelineSelector` restricts notifications to events from one pipeline:

```yaml
spec:
  pipelineSelector: nginx-demo  # only events from this pipeline
```

When empty, events from all Pipelines in the same namespace are delivered.
