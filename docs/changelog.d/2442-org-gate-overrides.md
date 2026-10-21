---
section: Documentation
---

- **Who can override an org gate** (#2442) — the security guide said teams cannot override org gates. They cannot remove or weaken them, but anyone who may override gates in the Pipeline's namespace can force-pass one, for at most `controller.gateOverrideMaxMinutes`, with a verified `createdBy` and a `GateOverridden` AuditEvent. [Who can override an org gate](guides/security.md#who-can-override-an-org-gate) names the chart roles that grant it (`<fullname>-approver` and `<fullname>-admin`, and the built-in `edit` and `admin` with `rbac.userRoles.aggregateToDefaultRoles`), and how to keep it from teams.
