---
section: Added
---

- **A `GateChanged` AuditEvent when a gate changes under a version in flight** (#2144) — when a PolicyGate that applies to a promoting version is created, edited or deleted and the version's gates get the change, kardinal records it once per version and rebuild: `spec.gates` lists up to 20 changed templates with the old and new digest and generation of the version's gate copies and the field manager the last writer declared (not verified), and `spec.gatesOmitted` counts the rest, so a bulk policy sync cannot crowd other audit records out. Gate copies of versions built before this release are the baseline and record nothing. Receipts and compliance evidence can show which rule held a version, and when it changed ([Events written automatically](guides/security.md#events-written-automatically)).
