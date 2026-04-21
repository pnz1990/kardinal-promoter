# Spec: feat(controller): NotificationHook CRD for outbound event notifications (issue #914)

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 1: Kargo parity`
- **Implements**: No outbound event notifications (🔲 → ✅)

## Graph-first analysis
This reconciler is valid under Graph-first rules:
- It is an **Owned node**: reads NotificationHook spec + watches Bundle/PolicyGate/PromotionStep.
- It writes only to its own CRD status (status.lastSentAt, status.lastEvent, status.failureMessage).
- HTTP calls happen at-most-once per event transition (idempotent via last-sent tracking in status).
- time.Now() is only called inside a CRD status write — no logic leak.
- No cross-CRD status mutations, no exec.Command, no in-memory state.
Pattern: analogous to PromotionStep reconciler which makes GitHub API calls on state transitions.

## Zone 1 — Obligations (falsifiable)

1. **O1 — NotificationHook CRD**: A `NotificationHook` CRD exists with:
   - `spec.webhook.url` (required string): HTTPS URL to POST to.
   - `spec.webhook.authorizationHeader` (optional string): value for the `Authorization` header.
   - `spec.events` (required []string): subset of `["Bundle.Verified", "Bundle.Failed",
     "PolicyGate.Blocked", "PromotionStep.Failed"]`.
   - `spec.pipelineSelector` (optional string): if set, only events from the named pipeline trigger delivery.
   - `status.lastSentAt` (optional RFC3339 string): timestamp of the last successful delivery.
   - `status.lastEvent` (optional string): last event type that was delivered.
   - `status.failureMessage` (optional string): last delivery failure message.

2. **O2 — NotificationHookReconciler**: A reconciler for NotificationHook watches Bundle,
   PolicyGate, and PromotionStep objects. On each reconcile, it determines whether a
   new notification event has occurred and is not yet delivered, fires the webhook,
   and writes the result to status.

3. **O3 — Bundle.Verified notification**: When a Bundle transitions to `Phase=Verified`
   AND the NotificationHook includes `"Bundle.Verified"` in spec.events, the reconciler
   fires the webhook exactly once (idempotent: tracked via status.lastSentAt + event key).

4. **O4 — Bundle.Failed notification**: When a Bundle transitions to `Phase=Failed`
   AND the NotificationHook includes `"Bundle.Failed"` in spec.events, the reconciler
   fires the webhook.

5. **O5 — PolicyGate.Blocked notification**: When a PolicyGate transitions to `ready=false`
   (first block, not every re-eval — tracked via the gate's status.lastEvaluatedAt)
   AND the NotificationHook includes `"PolicyGate.Blocked"` in spec.events, the reconciler
   fires the webhook.

6. **O6 — PromotionStep.Failed notification**: When a PromotionStep transitions to
   `status.state=Failed` AND the NotificationHook includes `"PromotionStep.Failed"`,
   the reconciler fires the webhook.

7. **O7 — Idempotency**: If the reconciler is restarted mid-delivery, it does NOT
   re-fire a webhook that was already successfully delivered (as recorded in status.lastSentAt
   + status.lastEvent).

8. **O8 — Webhook payload**: The POST body is a JSON object with fields:
   `event` (string), `pipeline` (string), `bundle` (string, if applicable),
   `environment` (string, if applicable), `message` (string), `timestamp` (RFC3339).

9. **O9 — Test coverage**: Unit tests cover O3, O4, O7 (idempotency), and O8 (payload shape).

10. **O10 — User docs**: `docs/notifications.md` documents the NotificationHook CRD with
    a Slack webhook example.

## Zone 2 — Implementer's judgment

- HTTP client timeout (10s recommended)
- Whether to retry on delivery failure (no retry in v1 — record failure in status)
- Exact JSON field names in the webhook payload
- Whether spec.pipelineSelector matches by label or by name (name is simpler)

## Zone 3 — Scoped out

- Email / PagerDuty / Teams adapters (webhook-only in v1)
- Template expressions in the payload body
- Retry with exponential backoff
- Notification batching (one webhook per event)
- UI for NotificationHook management
