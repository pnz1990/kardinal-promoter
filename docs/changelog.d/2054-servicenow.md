---
section: Added
---

- **ServiceNow change requests** (#2054, [ServiceNow](change-requests.md#servicenow)). `provider: servicenow` opens `change_request` records through the Table API and reads their state, approval and approvers. A record that a lost response or a restart left behind is found by its `correlation_id` before any new create. The default Change Management states are mapped to `pending`, `approved`, `rejected`, `cancelled` and `closed`, and `spec.servicenow.states` maps an instance's own state model. HTTP 401 and 403 mark the credential invalid, and 429 and 5xx back off to the instance's Retry-After. Redirects are never followed. `changeRequest.caBundle` trusts an internal CA for these calls only.
