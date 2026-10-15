---
section: Security
---

- **`health.refresh` refreshes only the Pipeline's own GitOps objects** (#1781) — a Pipeline could name any Application or Kustomization in `health.argocd` or `health.flux` and have the controller annotate it for a refresh, another team's included. The controller now refreshes only an object in the Pipeline's namespace or deploying there in this cluster (`spec.destination.namespace` of an in-cluster Application, `spec.targetNamespace`), a Kustomization's source only when it is in the Kustomization's namespace or in scope itself, and those that `health.refresh.namespaces` (`--health-refresh-namespaces`, `pipelineNamespace=namespace` rules) admits. Any other refresh is skipped with `refresh skipped (...)` and a `GitOpsRefreshSkipped` Event, and the step waits for the tool's poll. A remote cluster's refresh (`health.kubeconfigSecretRef`) uses the kubeconfig's identity, as before ([Sync on promotion](health-adapters.md#sync-on-promotion))
