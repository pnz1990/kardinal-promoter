---
section: Changed
---

- **The agent index is rebuilt only for the AgentIdentity changes it depends on** (#2431, W0 of [agent budgets](design/24-agent-budgets.md)). A create, a delete, a spec change, or a change of the status the index reconciler writes (an AgentIdentity's `ready`, `reason`, `observedGeneration` and Ready condition) rebuilds it. A status changed by another writer is put back. A label, an annotation or another controller's status field no longer re-runs the build over every AgentIdentity. This prepares for v0.14's agent budgets, whose usage is written to each agent's AgentIdentity status on every promotion.
