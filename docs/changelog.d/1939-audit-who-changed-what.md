---
section: Added
---

- **The audit trail names who paused, resumed, released, rejected, froze and lifted** (#1939) — new AuditEvents `PipelinePaused` and `PipelineResumed` (a Pipeline, or one fleet), and `FreezeStarted` and `FreezeLifted`, each naming who did it and their verified actor stamp. `kardinal pause`, `resume` and `unhold` take `--reason`. `HoldReleased` now names who released the hold, and `HoldCreated` and `BundleRejected` carry the actor in `spec.actor`. kardinal records who in the same write as the change (the Pipeline's new `spec.lastChange`, a freeze's `spec.freeze.lifted`), and the chart's admission policies admit only the requester's own name and stamp there (the new `<release>-actor-records` policy). A change made with kubectl is recorded too, without an actor ([Audit logging](guides/security.md#audit-logging), [Pause and Resume](pause.md#who-paused-and-why)).
