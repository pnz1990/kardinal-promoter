# 07: Distributed Architecture

> Status: Outline
> Depends on: 01-graph-integration, 03-promotionstep-reconciler
> Blocks: nothing (additive to standalone mode)

Control plane / agent split for multi-cluster deployments behind firewalls.

## Scope

- Binary split: kardinal-controller (control plane) vs kardinal-agent (per shard)
  - Controller handles: Pipeline reconciler, PolicyGate reconciler, Graph generation, Bundle lifecycle
  - Agent handles: PromotionStep reconciler only (same code as standalone)
- Shard routing: `kardinal.io/shard` label on PromotionStep CRs, `--shard` flag on agent
  - Controller sets the label during Graph generation based on `environment.shard` in the Pipeline CRD
  - Agent filters its watch to only PromotionSteps matching its shard
  - PromotionSteps without a shard label are reconciled by the controller (standalone behavior)
- Agent kubeconfig management
  - Agent connects to control plane API server to watch and update PromotionStep CRDs
  - Agent uses local cluster credentials for health checks (Deployment, Argo CD Application, etc.)
  - Control plane kubeconfig distributed as a Secret in the agent cluster
  - ServiceAccount + RBAC in control plane scoped to PromotionStep and Bundle status subresources
- Credential isolation
  - Git and SCM tokens: stored in the agent cluster, not the control plane
  - Health check credentials: local to the agent cluster
  - Control plane only holds: Pipeline, Bundle, PolicyGate, Graph CRDs (no workload cluster credentials)
- Standalone to distributed upgrade path
  - Phase 1: single binary, shard field ignored
  - Phase 2: deploy kardinal-agent to a target cluster with --shard, add shard field to Pipeline environments
  - No Pipeline CRD changes required (shard is already in the spec, just unused)
  - PromotionStep reconciler code is identical in both modes
- Security model
  - Agent to control plane: outbound connection only (agent phones home)
  - Control plane has no privileged access to agent clusters
  - Agent ServiceAccount in control plane: get/list/watch PromotionStep + update PromotionStep/status + get Bundle
- Observability
  - All CRDs (including agent-updated PromotionStep status) live in the control plane
  - kardinal-ui shows all shards from the control plane
  - Prometheus metrics from agents: how to aggregate (push to control plane? per-agent scrape?)
- Edge cases
  - Agent goes down: PromotionSteps in its shard stay in current state, resume on restart
  - Control plane goes down: agents continue reconciling existing PromotionSteps but no new ones are created
  - Shard reassignment: what happens if an environment's shard field changes while a promotion is in flight
