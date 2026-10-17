---
section: Security
---

- **The Graph identity reads nothing outside its namespace** (#1283) — the chart no longer ships `<fullname>-graph-reader`, which granted each Graph's ServiceAccount `get`, `list` and `watch` on Deployments, Argo CD Applications and Rollouts, Flux Kustomizations and Flagger Canaries in every namespace a Pipeline's health check named. The controller's `bind` grant is limited to the applier ClusterRole, and kro's aggregated `kro-watch` role no longer covers those health kinds ([RBAC requirements](guides/security.md#rbac-requirements)).
