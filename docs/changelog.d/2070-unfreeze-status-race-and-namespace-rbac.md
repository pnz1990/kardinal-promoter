---
section: Fixed
---

- **`kardinal unfreeze` of a freeze that just started no longer fails with "changed while it was being lifted"** (v0.12 release gate). The controller records a freeze as it starts, and that write moved the ResourceVersion that unfreeze's delete is conditioned on. Unfreeze now lifts the freeze again when only its status or metadata changed. A freeze whose spec changed (frozen again with a new reason or end) is still kept.
- **In namespace mode (`controller.watchNamespace`), the chart's cluster-wide ClusterRole holds only cluster-scoped kinds again** (v0.12 release gate). The controller reads the `kubernetes` Service's EndpointSlice, so that an AgentRun's NetworkPolicy can reach the API server. In namespace mode it now reads it through a Role in the `default` namespace.
