---
section: Security
---

- **A relinked automatic rollback is never adopted** (#2187 follow-up). `onHealthFailure: rollback` and a RollbackPolicy look for an existing automatic rollback of the failing version first, so one failing version gets one rollback. They found it by its `kardinal.io/rollback-from` annotation and labels, which anyone who may patch Bundles can rewrite, also on the controller's own rollbacks. Relinking the finished rollback of an earlier failure to the version failing now made kardinal take it for that version's rollback: no rollback was created, the environment stayed on the failing version, and the record named the wrong Bundle. kardinal now finds an existing rollback only by its name, one of the names the controller creates it at for the failing version, with the controller's stamp and the pipeline in `spec.pipeline` ([What Happens in Git](rollback.md#what-happens-in-git)).
