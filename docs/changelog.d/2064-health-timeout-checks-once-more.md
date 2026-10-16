---
section: Fixed
---

- **A late health check no longer fails a healthy environment** (#2064). Before, a promotion step reconciled after its `health.timeout` had passed applied `onHealthFailure` using the result it had read earlier. This happens when the controller is down or behind for longer than the timeout. A rollout that had become healthy in the meantime was failed, and with `onHealthFailure: rollback` it was rolled back. Now the step checks the environment once more before it fails. A bake whose deadline has passed still fails as before.
