---
section: Fixed
---

- **A verify gate, or an approval gate that only reads `approvals.*`, works next to a gate with an approval policy** (#2271): in a version's Graph that also held a gate with `spec.approval`, such a gate's instance was refused by the API server (`spec.approval` rendered as null) and never created, so the environment waited for ever. This covered, for example, an agent verifier on an environment where kardinal adds its approval gate for agents' versions. Each gate collection of the Graph now renders `spec.approval` for all its gates or for none ([Agents as verifiers](agentic/verify-with-agents.md)).
