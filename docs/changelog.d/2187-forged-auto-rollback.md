---
section: Security
---

- **A forged automatic rollback is never adopted** (#2187). `onHealthFailure: rollback` and a RollbackPolicy took any Bundle of the pipeline as the automatic rollback when it had the `kardinal.io/rollback` label, `kardinal.io/rollback-from` naming the failing version and its environment as target, at any name. Anyone who may create Bundles can set those fields, so they could pre-empt the rollback: the step said it was rolling back while the environment stayed on the failing version, or rolled forward to a version of their choosing through the normal gates. kardinal now adopts only a Bundle the controller created, with its actor stamp (`kind: controller`, `via: auto-rollback`) and creator, which the chart's admission lets no one else write ([What Happens in Git](rollback.md#what-happens-in-git)).
