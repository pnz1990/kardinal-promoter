---
section: Added
---

- **Change requests** (#2053, [Change Requests](change-requests.md)). A `ChangeRequest` with `perPromotion: true` is a template. Each Bundle's Graph creates an instance of it for every environment whose gates read `changeRequests.<name>`, once that environment's upstreams are Verified. The ChangeRequest controller opens the instance's ticket, or links one by its key, and writes the ticket's state to the instance's status. A gate waits on that state, for example `changeRequests["prod-change"].state == "approved"`.
  - **Never two tickets, never another promotion's.** The ticket carries the promotion's correlation id, and every create is searched for first and announced in status beforehand. A search adopts only a ticket that carries this promotion's correlation id or label; an answer that ignored part of the query is an error, neither adopted nor followed by a create (`ProviderError`, reason `ProviderIgnoredFilter`, retried after five minutes) (#2140).
  - **Fail closed.** A reading older than two poll intervals is stale, and `state` then reads `unknown`. Provider errors and HTTP 429 back off to `status.retryAt`.
  - **Only kardinal writes an instance.** The graph-objects admission policy refuses anyone else.
  - **Provider calls are capped** by `changeRequest.maxInflight` and `changeRequest.maxInflightPerNamespace`.
  - **A version that stops promoting stops the reads.** The instances of a Verified or Failed version are suspended, and those of a Superseded, Rejected or gone version stop reading their tickets, with the `Ready` condition `VersionFinal` (#2167). kardinal never closes or cancels a ticket.
  - The providers are ServiceNow (#2054) and Jira (#2055).
