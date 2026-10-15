---
section: Upgrade
---

- **Freezes get an audit finalizer** (#1939) — the controller adds `kardinal.io/freeze-audit` to each freezing ChangeWindow and needs `patch` on `changewindows` (the chart grants it); a deleted freeze stays until its `FreezeLifted` AuditEvent is written. A Pipeline already paused at the upgrade is recorded without an AuditEvent; a freeze already in place gets its `FreezeStarted` record, dated when the freeze was created. Apply the new CRDs before the chart: `pipelines` (`spec.lastChange`, the `status.observedPaused*` fields, a hold's `actor`), `bundles` (`spec.rejected.actor`), `changewindows` (`spec.freeze.actor`, `spec.freeze.lifted`) and `auditevents` (the new actions) change.
