---
section: Added
---

- **Agents' promotions wait for their budget** (#2434, W3 of [agent budgets](design/24-agent-budgets.md)): with `--agent-budgets`, a PromotionStep of a version an agent made waits in Pending, with `status.budget` (Waiting, then Granted) and a message such as `waiting for agent fixer's budget (rule prod: versionsPerDay); kardinal budget fixer`, until the agent's ledger grants it; it is never failed for its budget. People's, bots' and the controller's versions are never held. A step that goes on under a budget exemption writes one `BudgetExempted` AuditEvent naming who made the exemption, why and until when (apply the AuditEvent CRD for the new action). The budget reconciler now stops retrying a ledger write the API server refuses until the next change, and says so in the ledger's `LedgerFull` condition (reason `WriteRefused`). Off by default: the chart's switch follows (W6).
