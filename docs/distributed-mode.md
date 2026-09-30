# Multi-Cluster

Distributed mode (`kardinal-agent` and `shard`) is **not supported** and was removed. It was never shipped: there was no agent image or chart, and the agent read PromotionSteps and ran health checks against one API server, so it could not run in a spoke cluster.

## Supported: kardinal next to the GitOps hub

Run one kardinal controller in the hub cluster, next to the Argo CD or Flux that manages your workload clusters. Declare one environment per cluster or region (for example `prod-eu` and `prod-us`), and use `wave` or `dependsOn` to promote them in parallel. kardinal writes to git and reads health from the hub's objects; it holds no credentials for the workload clusters.

- **Argo CD hub:** `health.type: argocd` on the environment's Application in the hub.
- **Flux hub:** `health.type: flux` on a Kustomization in the hub that targets the remote cluster with `spec.kubeConfig.secretRef`.

See [Health Adapters: Remote Clusters](health-adapters.md#remote-clusters) for the configuration.

**Not covered:** a spoke the hub cannot reach, such as a cluster with its own Argo CD or Flux that is not managed from the hub, has no kardinal health check.

## Upgrading from distributed mode

- Remove `shard:` from every Pipeline environment. It is rejected: the Pipeline is `Ready=False` (reason `NotImplemented`), `kardinal validate` fails, and a PromotionStep left over from distributed mode fails with `shard is not supported`. Once it is removed, the controller reconciles every environment.
- Remove the chart value `controller.shard` and the `--shard` flag (or `KARDINAL_SHARD`). The chart schema rejects the value, and the controller exits at startup when the flag or variable is set.
- Delete any `kardinal-agent` Deployment you built and ran yourself.
- Remove `health.cluster`. It is also rejected; use the Argo CD or Flux hub instead.

The withdrawn design is kept for history in [design 07](design/07-distributed-architecture.md).
