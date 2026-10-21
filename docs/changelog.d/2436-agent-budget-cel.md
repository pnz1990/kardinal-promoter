---
section: Added
---

- **`budget.*` in gate expressions** (#2436, W5 of [agent budgets](design/24-agent-budgets.md), [reference](reference/cel-context.md#budget)): with `--agent-budgets`, a PolicyGate's expression or `approval.requiredWhen` can read the version's agent's budget for its environment, such as `budget.applies && budget.env.versions.used * 2 >= budget.env.versions.limit` to have a person approve once half the day's budget is spent. The values are the counts the controller grants by, and a change of them re-evaluates only the gates that read them. Every key is present for people's versions. `kardinal policy simulate --as-agent` reads the agent's current counts. A gate shows a budget; the controller's grant enforces it.
