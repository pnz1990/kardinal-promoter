---
section: Upgrade
---

- **Apply the new CRDs before the chart** — as for every release: `agentidentities` and `agentindexes` are new, and `bundles`, `approvals`, `policygates`, `auditevents`, `pipelines` and `promotionsteps` gain the actor fields. Existing objects have no actor and keep working: they show by their `kardinal.io/created-by`, and nothing v0.11.0 admitted is refused unless it names a false actor or comes from a registered agent. For a few seconds after the upgrade, until the new controller has built the agent index, new versions, Approvals and gate overrides are refused with "kardinal's agent index is not ready yet"; retry them
