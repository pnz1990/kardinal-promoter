---
section: Added
---

- **The agent index says how full it is** (#2076). Past its 1 MiB bound the index is not Ready, and kardinal refuses every write the actor policy checks, people's included. Until now nothing showed the bound coming.
  - **Status:** the AgentIndex now records `status.bytes` (its entries, as the bound counts them, also when over it) and `status.maxBytes`.
  - **Condition:** a `NearFull` condition turns True from 80% of the bound. Its message gives the size, the share and how many more AgentIdentities of the average size fit.
  - **`kubectl get agentindex`** shows Bytes, Max and NearFull.
  - **Metrics:** the leader exports `kardinal_agent_index_bytes` and `kardinal_agent_index_max_bytes`.
  - **Alerts:** the chart's PrometheusRule adds `KardinalAgentIndexNearFull` (warning, 80% for 15 minutes) and `KardinalAgentIndexFull` (critical). See [Agent identities](actors.md) and [Monitoring](guides/monitoring.md#included-alerts).
