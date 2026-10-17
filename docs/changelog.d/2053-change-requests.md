---
section: Added
---

- **Change requests** (#2053, [Change Requests](change-requests.md)). A `ChangeRequest` with `perPromotion: true` is a template. Each Bundle's Graph creates an instance of it for every environment whose gates read `changeRequests.<name>`, once that environment's upstreams are Verified. The ChangeRequest controller opens the instance's ticket, or links one by its key, and writes the ticket's state to the instance's status. A gate waits on that state, for example `changeRequests["prod-change"].state == "approved"`.
  - **Never two tickets.** The ticket carries the promotion's correlation id, and every create is searched for first and announced in status beforehand.
  - **Fail closed.** A reading older than two poll intervals is stale, and `state` then reads `unknown`. Provider errors and HTTP 429 back off to `status.retryAt`.
  - **Only kardinal writes an instance.** The graph-objects admission policy refuses anyone else.
  - **Provider calls are capped** by `changeRequest.maxInflight` and `changeRequest.maxInflightPerNamespace`.
  - The providers are ServiceNow (#2054) and Jira (#2055).
