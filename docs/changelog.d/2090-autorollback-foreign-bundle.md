---
section: Fixed
---

- **Automatic rollback adopts only its own Bundle** (#2090). `onHealthFailure: rollback` and a RollbackPolicy took any Bundle they found at the rollback's name as the rollback, even one a person or CI created there. When both of its names held other Bundles, they retried forever and left the environment on the failing version. A Bundle there now counts only when the controller created it as that rollback: its actor stamp and creator, which only the controller can write (#2187), its `spec.pipeline`, and the environment as target; the name ties it to the failing version (#2223). Otherwise the rollback takes the environment's own name. When that one is taken too, the step stops at `AbortedByAlarm` (a RollbackPolicy refuses with reason `NameTaken`), naming the Bundle in the way ([What Happens in Git](rollback.md#what-happens-in-git)).
