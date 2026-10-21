---
section: Fixed
---

- **An upgrade's GateChanged records are kept even when the status write races** (#2443).
  - **Record first.** When the upgrade renders a promoting version's Graph again, the controller now stores that re-render's GateChanged AuditEvents in the Bundle's outbox first. These record the gate rules that changed under the version, and the verify gates that count only agents (#2411). Only then does it write the Graph with the current render version.
  - **The bug it fixes.** Before, it wrote the Graph first. A status write that then conflicted, or a controller that stopped at that moment, lost the records for good: the next reconcile found the Graph current. v0.13.0 listed this as a known limitation.
  - **Refused records.** A record the API server refuses in a dry-run create (Invalid, BadRequest or Forbidden) is dropped, so the re-render never waits on it. The drop raises an `AuditRecordRefused` Warning Event on the Bundle and counts in `kardinal_audit_events_dropped_total{reason="refused"}`.
  - **Transient errors.** Any other error keeps the Graph as the earlier controller left it, and the controller retries after 30 seconds.
  - **Caveat.** Because the record is stored first, it can describe a change that never happens as recorded:
    - a template edited between the reconcile that stores the record and the one that renders the Graph again goes straight to its newer rule;
    - a controller rolled back to v0.12 after storing the record writes it but never renders the Graph again.
