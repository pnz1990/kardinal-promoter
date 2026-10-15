---
section: Development
---

- **Agents in the scenarios** (`test/scenario`, #1867) — every scenario World runs the agent registry and admits each write through the API server's ValidatingAdmissionPolicy plugin with the chart's identity and actor policies, as its writer (the controller, kro's Graph ServiceAccount, the approver, the creator). The matrix ships versions as a person, a bot, an agent by ServiceAccount and an agent by group, and checks the stamp on the version, its AuditEvents and its PRs. Eight more rows check refusals: a forged stamp, an agent posing as a person, a suspended agent, an agent out of scope, a conflicting AgentIdentity, and the agent index missing, not built or over its bound. Each row also checks recovery. An agent load in the enterprise suite registers 2,000 AgentIdentities, with 50 agents shipping in bursts across 20 teams, and checks 4,000 admissions against `pkg/actor`.
