---
section: Fixed
---

- **A replica's start-up build of the agent index cannot undo a newer one** (#1917) — every replica built the AgentIndex once at start; one building from a stale cache could overwrite the leader's newer index, for example a suspension just applied, until the leader's next build. A replica now builds only an index that was never built, and every index write is refused with a conflict when the index changed since it was read.
