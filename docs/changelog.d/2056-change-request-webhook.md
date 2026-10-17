---
section: Added
---

- **Change request webhook trigger** (#2056, [Webhook trigger](change-requests.md#webhook-trigger)). A template with `spec.webhook` gives ServiceNow or Jira a URL, `POST /webhook/changerequests/<namespace>/<template>/<token>`, that makes its instances read their tickets at once instead of at the next poll. The trigger only sets `kardinal.io/refresh`, and its body is never read for state. The token is compared in constant time, and every failure is the same 401. A replayed trigger costs nothing, a trigger never overrides a provider's Retry-After, and polling stays correct when a trigger is lost.
