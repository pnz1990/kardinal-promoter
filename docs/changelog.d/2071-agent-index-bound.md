---
section: Documentation
---

- **Why the agent index stays bounded at 4,000 AgentIdentities** (#2071). [Scale and limits](operate.md#why-the-bound-stays-at-4000-until-v014) says why admission cost grows with the index. It also says why none of the three ways checked to lift the bound ships in v0.13: an index per namespace, shards routed by a match condition, and ConfigMap shards. The planned index per namespace moves to v0.14, and the page no longer says the index can't report its size (it can since #2076).
