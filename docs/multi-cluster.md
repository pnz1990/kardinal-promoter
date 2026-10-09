# Multi-Cluster

Distributed mode (`kardinal-agent` and `shard`) is **not supported** and was removed. It was never shipped: there was no agent image or chart, and the agent read PromotionSteps and ran health checks against one API server, so it could not run in a spoke cluster.

## Supported: kardinal next to the GitOps hub

Run one kardinal controller. Declare one environment per cluster or region (for example `prod-eu` and `prod-us`), and use `wave` or `dependsOn` to promote them in parallel. kardinal writes to git, and checks each environment's health in one of two ways:

- **A kubeconfig Secret** (`health.kubeconfigSecretRef`): every health type reads its object in that cluster, so a cluster with its own Argo CD or Flux works too.
- **The GitOps hub's objects**, with no credentials for the workload clusters:
  - **Argo CD hub:** `health.type: argocd` on the environment's Application in the hub.
  - **Flux hub:** `health.type: flux` on a Kustomization in the hub that targets the remote cluster with `spec.kubeConfig.secretRef`.

See [Health Adapters: Remote Clusters](health-adapters.md#remote-clusters) for the configuration.

## Upgrading from distributed mode

- Remove `shard:` from every Pipeline environment. It is rejected: the Pipeline is `Ready=False` (reason `NotImplemented`), `kardinal validate` fails, and a PromotionStep left over from distributed mode fails with `shard is not supported`. Once it is removed, the controller reconciles every environment.
- Remove the chart value `controller.shard` and the `--shard` flag (or `KARDINAL_SHARD`). The chart schema rejects the value, and the controller exits at startup when the flag or variable is set.
- Delete any `kardinal-agent` Deployment you built and ran yourself.
- Remove `health.cluster`. It is also rejected; use `health.kubeconfigSecretRef` or the Argo CD or Flux hub instead.

The design was withdrawn in #1321.
