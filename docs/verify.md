# Verify a promotion

A promotion step is Verified only when its environment proves the new version works. kardinal
has five ways to decide that. They combine: every check you configure must pass, in this order,
before the next environment starts.

| You want to | Use | Page |
|---|---|---|
| Wait until the workload runs the new version and is healthy (always on) | `health` (`resource`, `argocd`, `flux`, `argoRollouts`, `flagger`) | [Health checks](health-adapters.md) |
| Check a workload in another cluster | `health.kubeconfigSecretRef`, or the Argo CD or Flux hub | [Remote clusters](health-adapters.md#remote-clusters) |
| Require the environment to stay healthy for a while, not just once | `bake.minutes` (with `bake.policy`) | [Pipeline reference](reference/pipeline.md#specenvironments) |
| Run tests or a migration as a Job | `hooks` (pre: before the promotion; post: after the health check) | [Hooks](hooks.md) |
| Judge metrics with Argo Rollouts AnalysisTemplates | `analysis` | [Analysis](analysis.md) |
| Hold the *next* environment on a metric (error rate, latency) | a `MetricCheck` read by a gate (`metrics.<name>`) | [Metric checks](metric-checks.md) |

The order inside one environment is: health check, then `bake`, then post hooks and analysis.
A metric gate is different: it does not verify the environment it runs in, it holds the
environment that waits for it.

When a check fails, `onHealthFailure` decides what happens: fail the step (default), stop it for
a person (`abort`), or roll back (`rollback`); see [Rollback](rollback.md#automatic-rollback).
