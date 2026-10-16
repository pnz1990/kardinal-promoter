---
section: Documentation
---

- **Scale and limits** (#2071): [Operate at scale](operate.md#scale-and-limits) now says what kardinal is tested at and supports.
  - Tested: 5,000 AgentIdentities, 200 Pipelines in 50 tenants, and bursts of agent-shipped versions.
  - Admission p99: 47 ms for agents' Bundles and people's Approvals, 0.2 ms for kro's gate writes.
  - The agent index takes about 210 bytes per AgentIdentity and is full at about 5,000.
  - Supported: up to 4,000 AgentIdentities per cluster.
