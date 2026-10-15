---
section: Fixed
---

- **A gate held by its approval policy no longer leads with its message** (#2017) — a gate whose expression passes but whose `approval` (or `verify`) policy holds it led its reason with `spec.message`, which explains a false expression. A registered agent's version at autonomy `approve` was held by `kardinal-agents-auto` with "the version's agent is not registered, suspended, or its AgentIdentity autonomy is deny (waiting for approvals: 0 of 1)". It now reads `waiting for approvals: 0 of 1 (version …)`, and the Pending step says `waiting for gate <instance>`, as docs/policy-gates.md describes.
