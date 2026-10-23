---
section: Added
---

- **The agent budget reconciler** (#2433, W2 of [agent budgets](design/24-agent-budgets.md)): with `--agent-budgets`, the controller counts each agent's promotions against its AgentIdentity `spec.budgets` in the ledger `status.usage.<ledger>` (`--agent-budget-ledger`, by default this release), and grants the steps that ask, in arrival order and within every limit, with a conditional write so two writers never grant past a limit. It seeds the ledger from promotions already under way, applies exemptions for at most 24 hours from when it first sees them, and never counts one an agent made, or one without an actor stamp (`ExemptionIgnored`, and a Warning event). A ledger stops granting before it passes 2,000 grants, 3,000 across the agent's ledgers, or 1 MiB of AgentIdentity (`LedgerFull`). Off by default; steps do not wait for their grant yet (the step hold, W3, follows).
