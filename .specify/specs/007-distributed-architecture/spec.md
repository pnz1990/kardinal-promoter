# Feature Specification: Distributed Architecture

**Feature Branch**: `007-distributed-architecture`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 001-graph-integration, 003-promotionstep-reconciler
**Design doc**: `docs/design/07-distributed-architecture.md`
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

In distributed mode, the `kardinal-agent` binary runs in workload clusters behind firewalls. It connects outbound to the control plane, watches PromotionSteps labeled with its shard, and executes them. The control plane handles all orchestration (Pipeline reconciliation, Graph generation, PolicyGate evaluation). Same PromotionStep reconciler code in both modes.

---

## User Scenarios & Testing

### User Story 1 — Agent only processes its shard (Priority: P1)

An agent started with `--shard=eu-cluster` processes only PromotionSteps with `kardinal.io/shard=eu-cluster`.

**Independent Test**: `go test ./pkg/reconciler/promotionstep/... -run TestShardFiltering`

**Acceptance Scenarios**:

1. **Given** a PromotionStep with `shard=eu`, and an agent with `--shard=eu`, **When** reconcile runs, **Then** it processes the step
2. **Given** a PromotionStep with `shard=eu`, and an agent with `--shard=us`, **When** reconcile runs, **Then** it returns immediately without processing
3. **Given** a PromotionStep with no shard label, and standalone mode (no `--shard`), **When** reconcile runs, **Then** it processes the step

---

### User Story 2 — Agent writes evidence to control plane (Priority: P1)

The agent updates PromotionStep status and Bundle status in the control plane K8s API.

**Acceptance Scenarios**:

1. **Given** an agent that completes a PromotionStep in an eu-cluster worktree, **When** it writes `status.state = Verified`, **Then** the control plane Bundle status is also updated with evidence

---

### User Story 3 — Agent reconnects after control plane outage (Priority: P2)

If the control plane API server is temporarily unavailable, the agent retries with backoff.

**Acceptance Scenarios**:

1. **Given** an agent that loses the control plane connection mid-PromotionStep, **When** the connection restores, **Then** the agent resumes reconciliation from `status.currentStepIndex`

---

## Requirements

- **FR-001**: `kardinal-agent` binary: PromotionStep reconciler + `--shard` filter + `--control-plane-kubeconfig`
- **FR-002**: Shard filtering MUST use a label selector on the informer (not in-memory filtering)
- **FR-003**: Agent RBAC: get/list/watch PromotionStep + update PromotionStep/status + get Bundle + update Bundle/status only
- **FR-004**: Git/SCM credentials MUST live in the agent cluster namespace (not control plane)
- **FR-005**: Agent has zero access to Pipeline, PolicyGate, or Graph CRDs

### Package Structure

```
cmd/kardinal-agent/
  main.go         # agent binary entrypoint, --shard flag
```

---

## Success Criteria

- **SC-001**: `go test ./cmd/kardinal-agent/... -race` passes
- **SC-002**: Shard filtering unit tests pass
- **SC-003**: RBAC manifests in `config/rbac/agent-role.yaml` contain only required verbs
