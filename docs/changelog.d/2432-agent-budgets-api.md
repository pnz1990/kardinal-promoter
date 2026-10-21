---
section: Added
---

- **The agent budgets API** (#2432, W1 of [agent budgets](design/24-agent-budgets.md)): `AgentIdentity.spec.budgets` (at most 16 rules, each with at least one of `versionsPerDay`, `maxConcurrent`, `fleetTargetsPerHour`, `maxUnavailable` and `fleetShare`, the four counts summing to at most 1,500), `spec.budgetExemptions` (at most 32), `status.usage` (one ledger per installation) and `PromotionStep.status.budget`. The CRDs validate them. Nothing enforces them yet: the controller, the chart switch `actors.budgets.enabled` (off by default) and the CLI follow in later v0.14 work. Every field is optional, so an AgentIdentity without budgets is unchanged.
