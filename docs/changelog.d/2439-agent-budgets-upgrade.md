---
section: Upgrade
---

- **Agent budgets** ([page](agent-budgets.md)): apply the CRDs before the chart, as every upgrade requires: AgentIdentity (`spec.budgets`, `spec.budgetExemptions`, `status.usage`), PromotionStep (`status.budget`), Pipeline (`status.environments[].budgetHeldBy`) and AuditEvent (the `BudgetExempted` action, also in the audit outboxes of Bundles, Pipelines, PolicyGates, PromotionSteps and ChangeRequests). Budgets are off (`actors.budgets.enabled: false`), so nothing changes until you turn them on; the chart adds the `<release>-agent-budgets`, `<release>-actor-budgets` and, while budgets are off, `<release>-agent-budgets-off` admission policies, and the `<release>-agent-budget-exempter` ClusterRole.
