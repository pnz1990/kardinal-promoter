---
section: Fixed
---

- **Namespace mode: the cluster-scoped ClusterRole holds only cluster-scoped kinds again.** The `get` on the `kubernetes` EndpointSlice, which AgentRuns need for their NetworkPolicy, was in `kardinal-promoter-cluster-scoped`, and EndpointSlices are namespaced. With `controller.watchNamespace`, it is now the Role `kardinal-promoter-apiserver-endpoints` in `default`, with its RoleBinding. Cluster mode is unchanged. Found by the v0.12 release gate (TestChart_WatchNamespace).
