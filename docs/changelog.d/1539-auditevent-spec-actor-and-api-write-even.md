---
section: Added
---

- **AuditEvent `spec.actor` and API write events** (#1539) — each AuditEvent names the verified actor of what it records, and every write through the UI API or the Bundle API is an `APIWrite` AuditEvent with the caller, the route and the outcome, refusals included (`controller.accessLog.auditEvents`, on by default; at most 50 a second per replica, drops counted in `kardinal_audit_events_dropped_total{kind="APIWrite"}`). Only the controller may write an AuditEvent that names an actor (`<release>-audit-actor`). `bundleName`, `pipelineName` and `environment` are now optional, one of them required ([Audit logging](guides/security.md#audit-logging))
