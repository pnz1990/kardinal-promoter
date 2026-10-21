---
section: Added
---

- **Agent budgets in the CLI** (#2438, W7 of [agent budgets](design/24-agent-budgets.md)):
  - `kardinal budget <agent>` shows what an agent has used of each budget, when room frees, which versions wait and the exemptions; `-o json` prints its ledgers.
  - `kardinal override <pipeline> <env> --budget --reason <why> [--bundle <version>]` lets one environment, or one version, through the agent's budget as you, for an hour by default (at most a day).
  - `kardinal status` shows "Held by agent fixer's budget" (the Pipeline's new `status.environments[].budgetHeldBy`), and `kardinal why` says what moves it on.
  - `kardinal doctor` says when budgets are kept but not enforced, and names a ledger no controller writes or one behind its spec.

  The controller also emits `BudgetExhausted` and `BudgetAvailable` events when a rule runs out of room or has room again. Apply the Pipeline CRD for the new status field.
