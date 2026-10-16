---
section: Upgrade
---

- **The DORA metrics in `Pipeline.status.deploymentMetrics` change on the first reconcile after the upgrade** (#1942, #1944) — no action is needed, but expect a step in dashboards and alerts that read them. The p50 and p90 lead time start at the commit for versions that carry `provenance.commitTime` (longer, since they now include the build) and leave rollback versions out. `deployments`, `changeFailureRateMillis` and `meanTimeToRestoreMinutes` count retired versions, so they cover the last 30 deployments instead of the version in flight, and a rolled-back deployment counts as failed. `autoRollbackRateMillis` counts only the rollbacks the controller started (`kardinal rollback` moves to the new `rollbackRateMillis`). Versions created before the upgrade have no commit time, so their lead time still starts at their creation ([Monitoring](guides/monitoring.md#change-failure-rate-and-time-to-restore)).
