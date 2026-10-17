---
section: Upgrade
---

- **A rollback no longer re-deploys the old version to the environments upstream of its target** (#1311) — after the upgrade, `kardinal rollback <pipeline> prod` leaves test and uat on what they run when they verified the restored version before. If your process relied on a rollback also rolling the upstream environments back, roll each of them back on its own (`kardinal rollback <pipeline> uat`). Rollbacks and promotions created before the upgrade still go through the upstream environments ([Multi-Environment Rollback](rollback.md#multi-environment-rollback)).
