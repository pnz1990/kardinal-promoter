---
section: Added
---

- **`health.refreshTarget`: sync on promotion for Rollouts, Canaries and Deployments** (#2007) — an environment checked with `health.type: argoRollouts`, `flagger` or `resource` named no GitOps object, so `health.refresh` asked nothing and every promotion waited for Argo CD's or Flux's poll (minutes on a managed Argo CD). `health.refreshTarget` names the Argo CD Application (`argocd`) or Flux Kustomization (`flux`) that deploys the environment, in the controller's cluster, also when the health check reads another cluster through `health.kubeconfigSecretRef` (a hub's Application deploying to a spoke). The step asks it to sync once, with the controller's identity, within the refresh scope of `health.refresh.namespaces` ([Sync on promotion](health-adapters.md#sync-on-promotion))
