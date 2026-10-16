---
section: Fixed
---

- **Each failing environment gets its own automatic rollback** (#2082). Before, when two environments of one Bundle failed at the same time with `onHealthFailure: rollback` or a RollbackPolicy, only the first was rolled back. This happens with fan-out, waves, or a fleet with `maxConcurrent` above 1. The others reported the first environment's rollback Bundle as their own and stayed on the failing version. The first environment's rollback is still named `<bundle>-rollback-alarm` (or `-policy`), and each other environment's is now `<bundle>-rollback-alarm-<environment>`.
