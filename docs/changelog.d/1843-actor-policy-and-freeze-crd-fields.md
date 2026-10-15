---
section: Upgrade
---

- **New actor-policy and freeze fields** (#1843) — apply the CRDs for these too: `changewindows` (`spec.freeze`, its status), `policygates` (`approval.requiredWhen`, `allowAgents`, `allowedAgents`, `status.approvalRequired`), `pipelines` (`environments[].agents`) and `auditevents` (the `FreezeExempted` action). Graphs built before the upgrade keep their gates; a freeze holds their steps too
