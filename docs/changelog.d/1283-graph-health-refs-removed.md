---
section: Removed
---

- **Graph health ref nodes, the reader ClusterRole and its RoleBindings, `--graph-reader-namespaces`, `--graph-reader-clusterrole` and `graph.readerNamespaces`** (#1283) — a Graph no longer has a read-only ref node per environment for its health object, and the Graph ServiceAccount is no longer bound in `argocd`, `flux-system` or any namespace other than its own. Those nodes only fed the Graph's readiness: the PromotionStep reconciler checks health, as before, so promotions behave the same. Setting a removed flag stops the controller with a message saying so, and setting `graph.readerNamespaces` fails the install or upgrade ([RBAC requirements](guides/security.md#rbac-requirements)).
