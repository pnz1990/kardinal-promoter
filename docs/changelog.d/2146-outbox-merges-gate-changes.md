---
section: Fixed
---

- **A full audit outbox merges gate-change records before it drops any** (#2146). During a long AuditEvent write outage, repeated PolicyGate edits under a version in flight could fill its 32-entry outbox with `GateChanged` records and push out its `PromotionStarted` or `GateOverridden` record. A full outbox now merges the version's unwritten `GateChanged` records first, into one that runs each template from its first recorded digest to its last ([Audit outbox](guides/security.md#audit-outbox)).
