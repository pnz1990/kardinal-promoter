---
section: Fixed
---

- **An upgrade's GateChanged records are kept even when the status write races** (#2443). When the upgrade renders a promoting version's Graph again, the controller now stores the GateChanged AuditEvents for that re-render in the Bundle's outbox first. These are the gate rules that changed under the version, and the verify gates of agents only (#2411). Only then does it write the Graph with the current render version. Before, it wrote the Graph first. A status write that then conflicted, or a controller that stopped at that moment, lost the records for good, because the next reconcile found the Graph current. v0.13.0 listed this as a known limitation.
