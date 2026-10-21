---
section: Added
---

- **Agent budgets pace fleets** (#2435, W4 of [agent budgets](design/24-agent-budgets.md)): with `--agent-budgets`, a version an agent made gets its fleet targets' `maxConcurrent` and `maxUnavailable` lowered to its budget rules' `maxConcurrent`, `fleetShare` of the fleet (rounded up, at least one) and `maxUnavailable`, the strictest winning, so its fleet creates no more steps than its budget lets run instead of creating them only to wait. A person's version's Graph is unchanged. A looser budget reaches a Graph in flight when it is rebuilt; a tighter one applies at once through the grant.
