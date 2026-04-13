# Monitoring Guide

kardinal-promoter exposes Prometheus metrics at `:8080/metrics` (the default `metricsBindAddress`). The endpoint is scraped by standard Prometheus installations.

---

## Scrape Configuration

Add kardinal-promoter to your Prometheus scrape config:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: kardinal-promoter
    static_configs:
      - targets: ['kardinal-promoter-controller.kardinal-system.svc.cluster.local:8080']
    metrics_path: /metrics
```

If you use the Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: kardinal-promoter
  namespace: kardinal-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: kardinal-promoter
  endpoints:
    - port: metrics
      path: /metrics
      interval: 30s
```

The Helm chart creates the Service with the `metrics` port (8080) automatically.

---

## Controller-Runtime Standard Metrics

kardinal-promoter uses [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime), which automatically exposes the following metrics:

### Reconciler metrics

| Metric | Type | Description |
|---|---|---|
| `controller_runtime_reconcile_total` | Counter | Total reconcile operations, labelled by `controller` and `result` (`success`, `error`, `requeue`) |
| `controller_runtime_reconcile_errors_total` | Counter | Total reconcile errors, labelled by `controller` |
| `controller_runtime_reconcile_time_seconds` | Histogram | Time spent per reconcile loop, labelled by `controller` |
| `controller_runtime_max_concurrent_reconciles` | Gauge | Configured max concurrent reconciles per controller |
| `controller_runtime_active_workers` | Gauge | Active reconcile goroutines per controller |

**Controller labels**: `bundle`, `promotionstep`, `policygate`, `metriccheck`

### Work queue metrics

| Metric | Type | Description |
|---|---|---|
| `workqueue_adds_total` | Counter | Items added to the work queue per controller |
| `workqueue_depth` | Gauge | Current work queue depth per controller |
| `workqueue_queue_duration_seconds` | Histogram | Time items spend in the queue before processing |
| `workqueue_work_duration_seconds` | Histogram | Time spent processing each item |
| `workqueue_retries_total` | Counter | Total retries per controller |

### Webhook metrics

| Metric | Type | Description |
|---|---|---|
| `controller_runtime_webhook_requests_total` | Counter | Bundle creation webhook calls by `result` |
| `controller_runtime_webhook_request_duration_seconds` | Histogram | Webhook latency |

---

## Go Runtime Metrics

Standard Go runtime metrics are also exposed:

| Metric | Description |
|---|---|
| `go_goroutines` | Current goroutine count |
| `go_memstats_alloc_bytes` | Heap memory in use |
| `go_gc_duration_seconds` | GC pause duration |
| `process_cpu_seconds_total` | CPU time consumed |

---

## Sample PromQL Queries

### Active bundles (Promoting or Available)

```promql
# Total bundle reconcile operations in the last 5 minutes
rate(controller_runtime_reconcile_total{controller="bundle"}[5m])
```

### Promotion step error rate

```promql
# Fraction of PromotionStep reconciles that error
rate(controller_runtime_reconcile_errors_total{controller="promotionstep"}[5m])
/
rate(controller_runtime_reconcile_total{controller="promotionstep"}[5m])
```

### PolicyGate evaluation rate

```promql
# PolicyGate evaluations per second
rate(controller_runtime_reconcile_total{controller="policygate"}[5m])
```

### Controller health: reconcile latency P99

```promql
histogram_quantile(0.99,
  rate(controller_runtime_reconcile_time_seconds_bucket{controller="promotionstep"}[5m])
)
```

### Work queue depth (are we falling behind?)

```promql
workqueue_depth{name=~"bundle|promotionstep|policygate"}
```

### Webhook latency P95

```promql
histogram_quantile(0.95,
  rate(controller_runtime_webhook_request_duration_seconds_bucket[5m])
)
```

---

## DORA Metrics via CLI

kardinal-promoter also exposes **DORA-style promotion metrics** via the `kardinal metrics` command (not Prometheus — these are computed from CRD history):

```bash
kardinal metrics --pipeline my-app --env prod --days 30
```

Output:
```
PIPELINE   ENV    PERIOD   DEPLOY_FREQ   LEAD_TIME     FAIL_RATE   ROLLBACKS
my-app     prod   30d      2.1/day       45m avg       3.2%        1
```

| Metric | Description |
|---|---|
| `DEPLOY_FREQ` | Bundles successfully promoted to the target environment per day |
| `LEAD_TIME` | Average time from Bundle creation to target environment verification |
| `FAIL_RATE` | Percentage of Bundles that reached `Failed` state |
| `ROLLBACKS` | Number of rollback Bundles in the period |

---

## Alerting Examples

### Controller is not reconciling

```yaml
groups:
  - name: kardinal
    rules:
      - alert: KardinalControllerNotReconciling
        expr: |
          rate(controller_runtime_reconcile_total{controller="bundle"}[10m]) == 0
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "kardinal BundleReconciler has not run in 10 minutes"

      - alert: KardinalHighReconcileErrors
        expr: |
          rate(controller_runtime_reconcile_errors_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "kardinal reconcile error rate > 0.1/s for {{ $labels.controller }}"

      - alert: KardinalWorkQueueBacklog
        expr: workqueue_depth{name=~"bundle|promotionstep|policygate"} > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "kardinal work queue depth > 100 for {{ $labels.name }}"
```

---

## Grafana Dashboard

A community Grafana dashboard is available at:

**Dashboard ID**: _pending submission to grafana.com_

In the meantime, import the following JSON panels manually:

**Panel: Reconcile rate by controller**
```json
{
  "targets": [{
    "expr": "sum by (controller) (rate(controller_runtime_reconcile_total[5m]))",
    "legendFormat": "{{controller}}"
  }],
  "type": "timeseries",
  "title": "Reconcile rate (ops/s)"
}
```

**Panel: Reconcile error rate**
```json
{
  "targets": [{
    "expr": "sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m]))",
    "legendFormat": "{{controller}} errors"
  }],
  "type": "timeseries",
  "title": "Reconcile errors (errors/s)"
}
```

---

## Changing the Metrics Port

Set `metricsBindAddress` in Helm values to use a different port:

```yaml
# values.yaml
metricsBindAddress: ":9090"
service:
  metricsPort: 9090
```

---

## Further Reading

- [Security Guide](security.md) — network policy, RBAC
- [CLI Reference](../cli-reference.md#kardinal-metrics) — `kardinal metrics` DORA command
- [Troubleshooting](../troubleshooting.md) — debugging stuck promotions
