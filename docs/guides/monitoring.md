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

## Alerting Rules — PrometheusRule (Helm)

kardinal-promoter ships a `PrometheusRule` resource that works with the [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator) (kube-prometheus-stack, Victoria Metrics Operator, etc.).

### Enable via Helm

```yaml
# values.yaml
prometheusRule:
  enabled: true
  # Match your Prometheus Operator's selector labels:
  additionalLabels:
    release: kube-prometheus-stack
```

Install or upgrade:

```bash
helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/kardinal-promoter/chart/kardinal-promoter \
  --set prometheusRule.enabled=true \
  --set 'prometheusRule.additionalLabels.release=kube-prometheus-stack'
```

### Included alerts

| Alert | Expression | Severity | Description |
|---|---|---|---|
| `KardinalControllerDown` | `up{job="kardinal-promoter"} == 0` for 5m | critical | Controller unreachable; promotions stalled |
| `KardinalHighReconcileErrors` | reconcile error rate > 0.1/s for 5m | warning | Reconciler failing; promotions may be stuck |
| `KardinalBundleReconcilerStalled` | no bundle reconciles for 10m | warning | Controller idle; possible leader election issue |
| `KardinalWorkQueueBacklog` | work queue depth > 100 for 5m | warning | Controller overloaded or starved of CPU |
| `KardinalPolicyGateReconcileSlow` | PolicyGate P99 latency > 10s | warning | Gate evaluations stale; promotions blocked late |
| `KardinalWebhookErrors` | webhook 5xx rate > 0 for 5m | warning | Bundle creation API failing; CI pipelines cannot promote |

Every alert includes a `runbook_url` annotation pointing to the relevant section in [troubleshooting.md](../troubleshooting.md).

### Manual alerting (without Prometheus Operator)

If you don't use the Prometheus Operator, copy the rules into your `prometheus.yml`:

```yaml
groups:
  - name: kardinal-promoter
    rules:
      - alert: KardinalControllerDown
        expr: up{job="kardinal-promoter"} == 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "kardinal-promoter controller is not scraping"
          runbook_url: "https://pnz1990.github.io/kardinal-promoter/troubleshooting/#controller-not-running"

      - alert: KardinalHighReconcileErrors
        expr: |
          sum by (controller) (
            rate(controller_runtime_reconcile_errors_total[5m])
          ) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "kardinal {{ $labels.controller }} reconcile error rate elevated"
          runbook_url: "https://pnz1990.github.io/kardinal-promoter/troubleshooting/#reconcile-errors"

      - alert: KardinalWorkQueueBacklog
        expr: workqueue_depth{name=~"bundle|promotionstep|policygate"} > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "kardinal work queue depth > 100 for {{ $labels.name }}"
          runbook_url: "https://pnz1990.github.io/kardinal-promoter/troubleshooting/#work-queue-backlog"
```

---

## Grafana Dashboard

kardinal-promoter ships a pre-built Grafana dashboard covering:

- **Promotion overview**: bundle phase counts (Verified/Failed/Superseded), step failure rate, gate blocks, reconcile errors
- **Throughput**: bundle phase rate and step terminal rate over time
- **Step latency**: P50/P99 per step type (git-clone, kustomize, open-pr, health-check), PR review latency, PromotionStep age
- **Policy gates**: gate evaluation rate and blocking duration histograms
- **Reconciler health**: reconcile rate, error rate, P99 latency, work queue depth per controller
- **Go runtime**: goroutines, heap memory, CPU usage

### Option 1 — Grafana sidecar (kube-prometheus-stack)

Enable via Helm when using a Grafana sidecar that auto-discovers labelled ConfigMaps:

```yaml
# values.yaml
grafanaDashboard:
  enabled: true
  sidecarLabel:
    grafana_dashboard: "1"   # match your Grafana sidecar's label selector
```

Install or upgrade:

```bash
helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/kardinal-promoter/chart/kardinal-promoter \
  --set grafanaDashboard.enabled=true \
  --set 'grafanaDashboard.sidecarLabel.grafana_dashboard=1'
```

Grafana will pick up the dashboard automatically within 60 seconds. Search for **"kardinal-promoter"** in the Grafana dashboard list.

### Option 2 — Manual import

Download the dashboard JSON from the repository:

```
config/monitoring/kardinal-promoter-dashboard.json
```

In Grafana: **Dashboards → Import → Upload JSON file**. Select your Prometheus datasource when prompted.

The dashboard UID is `kardinal-promoter-v1`. Importing a second time will overwrite the existing dashboard.

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
