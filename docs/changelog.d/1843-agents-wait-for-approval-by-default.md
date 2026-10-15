---
section: Upgrade
---

- **An agent's versions wait for a person by default** (#1843) — an AgentIdentity's `autonomy` defaults to `approve`. Every version a registered agent creates therefore waits in **each** environment, at the `kardinal-agents-<ceiling>` gate, until a person approves it there (`kardinal approve <pipeline> <env>`). The agent's sponsor does not count, and neither does another agent. To let an agent's versions promote without a person, set `autonomy: auto` on its AgentIdentity. A Pipeline environment's `agents` can only tighten this: `agents: auto`, or no `agents`, leaves the AgentIdentity's autonomy to decide, so it does not remove the approval, while `approve` and `deny` add their limit. Versions people create are not affected ([Autonomy per environment](agentic/policy.md#autonomy-per-environment)).
