# Item 027: MetricCheck CRD + Prometheus Evaluator + Soak Time in CEL

> **Stage**: Stage 15 (MetricCheck CRD and Upstream Soak Time)
> **Queue**: queue-013
> **Priority**: high
> **Size**: l
> **Depends on**: 010 (PolicyGate CEL evaluator)
> **dependency_mode**: merged

## Context

Stage 15 adds:
1. `MetricCheck` CRD for Prometheus-backed metric gates
2. `MetricCheckReconciler` that queries Prometheus and patches `status.lastValue` and `status.result`
3. CEL context extensions: `metrics.<name>.value`, `metrics.<name>.result`, `upstream.<env>.soakMinutes`
4. Sample gates: `error-rate-gate.yaml`, `upstream-soak-gate.yaml`
5. `kardinal policy simulate` extended to include metric values and soak times

The CEL extension for metrics MUST use only values already written to CRD status
fields (MetricCheck.status.lastValue). No direct Prometheus queries in CEL.
The MetricCheckReconciler writes values to CRD status — Graph reads them via Watch nodes.

## Acceptance Criteria

- `MetricCheck` CRD type with:
  - `spec.provider: prometheus`
  - `spec.query` (PromQL string)
  - `spec.threshold` (value, operator: `lt|gt|lte|gte|eq`)
  - `spec.interval`
  - `status.lastValue`, `status.lastEvaluatedAt`, `status.result`
- `MetricCheckReconciler` that:
  - Queries Prometheus via HTTP at `spec.prometheusURL`
  - Evaluates threshold and patches `status.result` (Pass/Fail)
  - Re-queues after `spec.interval`
- `metrics.Provider` interface with `PrometheusProvider` implementation
- CEL context extensions in PolicyGate evaluator:
  - `metrics.<name>.value` — current MetricCheck status.lastValue
  - `metrics.<name>.result` — MetricCheck status.result
  - `upstream.<env>.soakMinutes` — minutes since Bundle.status.environments[env].healthCheckedAt
- Updated sample gates in `config/samples/gates/`:
  - `error-rate-gate.yaml`: blocks if `metrics.error_rate.value > 0.01`
  - `upstream-soak-gate.yaml`: blocks if `upstream.uat.soakMinutes < 30`
- Unit tests:
  - MetricCheckReconciler with mock Prometheus server
  - CEL evaluator tests with metric context
  - `upstream.uat.soakMinutes` computed correctly from Bundle status timestamps
- Integration test: Prometheus running in kind cluster; error-rate gate passes when low, fails when high

## Files to Create/Modify

- `api/v1alpha1/metriccheck_types.go` — new CRD type
- `pkg/reconciler/metriccheck/reconciler.go` — new reconciler
- `pkg/reconciler/metriccheck/reconciler_test.go` — unit tests
- `pkg/cel/evaluator.go` — extend BundleContext with metrics and upstream soak
- `pkg/cel/evaluator_test.go` — new tests for metrics context
- `config/samples/gates/error-rate-gate.yaml` — new sample
- `config/samples/gates/upstream-soak-gate.yaml` — new sample
- `cmd/kardinal-controller/main.go` — register MetricCheckReconciler
- `config/rbac/` — ClusterRole for MetricCheck

## Tasks

- [ ] T001 Define MetricCheck CRD type with +kubebuilder markers
- [ ] T002 Write failing tests for MetricCheckReconciler
- [ ] T003 Implement MetricCheckReconciler with mock Prometheus interface
- [ ] T004 Write failing tests for CEL metrics context extension
- [ ] T005 Implement metrics/soak context in pkg/cel/evaluator.go
- [ ] T006 Create sample gate YAML files
- [ ] T007 Register MetricCheckReconciler in controller main.go
- [ ] T008 Update config/rbac/ for MetricCheck
- [ ] T009 Verify go test -race passes
