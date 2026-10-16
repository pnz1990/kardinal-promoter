---
section: Fixed
---

- **A hold of the running version is no longer called a rollback** (#2067). The HoldCreated and HoldReleased AuditEvents, and the message of a step the hold keeps out, said "held on rollback <bundle>" for every hold. A `kardinal hold` of what runs in the environment now says "held on <bundle>". Only the hold of a rollback (`kardinal rollback --hold`) says "on rollback", as `kardinal explain` already did.
