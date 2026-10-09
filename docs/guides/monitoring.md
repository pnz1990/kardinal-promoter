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
      - targets: ['kardinal-promoter.kardinal-system.svc.cluster.local:8080']
    metrics_path: /metrics
```

The Helm chart creates the Service with the `metrics` port (8080) automatically.

### Prometheus Operator

If you use the Prometheus Operator (for example kube-prometheus-stack), let the chart
create a ServiceMonitor:

```yaml
# values.yaml
serviceMonitor:
  enabled: true        # default false; needs the monitoring.coreos.com CRDs
  interval: 30s        # optional; empty uses the Prometheus default
  labels:              # optional; match your Prometheus serviceMonitorSelector
    release: kube-prometheus-stack
```

The ServiceMonitor scrapes the chart Service's `metrics` port over plain HTTP, in the
release namespace. Prometheus Operator sets the `job` label to the Service name, which
is the chart's full name (`kardinal-promoter` for a release named `kardinal-promoter`).
The alerts from `prometheusRule.enabled` select `job="<full name>"`, so they match this
ServiceMonitor with no extra settings. If you scrape with your own ServiceMonitor or a
static config instead, keep the job label equal to that Service name, or the
`KardinalControllerDown` alert fires.

---

## Controller-Runtime Standard Metrics

kardinal-promoter uses [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime), which automatically exposes the following metrics:

### Reconciler metrics

| Metric | Type | Description |
|---|---|---|
| `controller_runtime_reconcile_total` | Counter | Total reconcile operations, labelled by `controller` and `result` (`success`, `error`, `requeue`, `requeue_after`) |
| `controller_runtime_reconcile_errors_total` | Counter | Total reconcile errors, labelled by `controller` |
| `controller_runtime_reconcile_time_seconds` | Histogram | Time spent per reconcile loop, labelled by `controller` |
| `controller_runtime_max_concurrent_reconciles` | Gauge | Configured max concurrent reconciles per controller |
| `controller_runtime_active_workers` | Gauge | Active reconcile goroutines per controller |

**Controller labels**: `bundle`, `changewindow`, `graphcleanup`, `metriccheck`, `notificationhook`, `pipeline`, `policygate`, `promotionstep`, `prstatus`, `rollbackpolicy`, `scheduleclock`, `subscription`

### Work queue metrics

| Metric | Type | Description |
|---|---|---|
| `workqueue_adds_total` | Counter | Items added to the work queue per controller |
| `workqueue_depth` | Gauge | Current work queue depth per controller |
| `workqueue_queue_duration_seconds` | Histogram | Time items spend in the queue before processing |
| `workqueue_work_duration_seconds` | Histogram | Time spent processing each item |
| `workqueue_retries_total` | Counter | Total retries per controller |

---

## kardinal Metrics

The controller registers these on the same `/metrics` endpoint
(`pkg/reconciler/observability/metrics.go`):

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kardinal_bundles_total` | Counter | `phase` | Bundle phase transitions, labelled by the phase entered |
| `kardinal_steps_total` | Counter | `type` (always `PromotionStep`), `result` (`succeeded`, `failed`) | PromotionSteps reaching a terminal state |
| `kardinal_gate_evaluations_total` | Counter | `result` (`allowed`, `blocked`) | PolicyGate evaluations |
| `kardinal_pr_duration_seconds` | Histogram | — | Time from the PR opening (the `open-pr` step completing) to the merge the controller sees, observed once, when the step's move to `HealthChecking` is written |
| `kardinal_step_duration_seconds` | Histogram | `step` (step name, e.g. `git-clone`) | Duration of each promotion step, observed once, when the status that records it Completed or Failed is written. `wait-for-merge` lasts until the merge; `health-check` covers the health check and the bake |
| `kardinal_gate_blocking_duration_seconds` | Histogram | — | How long a PolicyGate was blocked before it allowed |
| `kardinal_promotionstep_age_seconds` | Histogram | — | PromotionStep age when it reaches a terminal state |

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

### Bundle reconcile rate

```promql
# Bundle reconciles per second
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
# PolicyGate evaluations per second, by result
sum by (result) (rate(kardinal_gate_evaluations_total[5m]))
```

### Promotion failure ratio

```promql
# Fraction of PromotionSteps that ended Failed in the last hour
sum(increase(kardinal_steps_total{result="failed"}[1h]))
/
sum(increase(kardinal_steps_total[1h]))
```

### Gates blocking promotions

```promql
# Share of PolicyGate evaluations that blocked
sum(rate(kardinal_gate_evaluations_total{result="blocked"}[15m]))
/
sum(rate(kardinal_gate_evaluations_total[15m]))
```

### PR review latency P90

```promql
histogram_quantile(0.9, sum by (le) (rate(kardinal_pr_duration_seconds_bucket[1d])))
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

---

## DORA Metrics via CLI

kardinal-promoter also exposes **DORA-style promotion metrics** via the `kardinal metrics` command (not Prometheus — these are computed from CRD history):

```bash
kardinal metrics --pipeline my-app --env prod --days 7
```

Output:
```
METRIC                 VALUE     NOTES
pipeline               my-app    (last 7 days)
target_env             prod
bundles_total          15
deployment_frequency   2.00/day  (14 verified in target env)
lead_time_avg          45m12s    (creation → prod verified, 14 samples)
change_fail_rate       6.7%      (1 failed / 15 total)
rollback_count         1
```

| Metric | Description |
|---|---|
| `bundles_total` | Bundles created in the window |
| `deployment_frequency` | PromotionSteps Verified in `--env` per day |
| `lead_time_avg` | Mean time from Bundle creation to Verified in `--env` |
| `change_fail_rate` | Failed Bundles divided by `bundles_total` |
| `rollback_count` | Rollback Bundles in the window |

These count only Bundles still in the cluster. Each Pipeline keeps its 50 newest finished
Bundles by default (`spec.historyLimit`), so busy Pipelines show a short window.

When `--env` is the Pipeline's last environment and `--days` is 30 (the defaults, whether
the flags are given or not), the command prints the controller's own figures from
`Pipeline.status.deploymentMetrics` instead, when they are present: `rollouts_last_30d`, `p50_commit_to_prod`,
`p90_commit_to_prod`, `auto_rollback_rate`, `operator_intervention_rate` and
`stale_prod_days`, over the last 30 Bundles Verified in the last environment.

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
helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --set prometheusRule.enabled=true \
  --set 'prometheusRule.additionalLabels.release=kube-prometheus-stack'
```

### Included alerts

| Alert | Expression | Severity | Description |
|---|---|---|---|
| `KardinalControllerDown` | `up{job="kardinal-promoter"}` is 0 or absent for 5m | critical | Controller unreachable; promotions stalled |
| `KardinalHighReconcileErrors` | reconcile error rate > 0.1/s for 5m | warning | Reconciler failing; promotions may be stuck |
| `KardinalBundleReconcilerStalled` | Bundles queued but none reconciled for 10m | warning | Controller stuck; possible leader election issue |
| `KardinalWorkQueueBacklog` | work queue depth > 100 for 5m | warning | Controller overloaded or starved of CPU |
| `KardinalPolicyGateReconcileSlow` | PolicyGate P99 latency > 10s | warning | Gate evaluations stale; promotions blocked late |

Every alert includes a `runbook_url` annotation pointing to the relevant section in [troubleshooting.md](../troubleshooting.md).

### Manual alerting (without Prometheus Operator)

If you don't use the Prometheus Operator, put these rules in a rule file listed under
`rule_files` in `prometheus.yml`. This block has three of the five alerts; the full set is in
`chart/kardinal-promoter/templates/prometheusrule.yaml`.

```yaml
groups:
  - name: kardinal-promoter
    rules:
      - alert: KardinalControllerDown
        expr: absent(up{job="kardinal-promoter"}) == 1 or up{job="kardinal-promoter"} == 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "kardinal-promoter controller is not scraping"
          runbook_url: "https://pnz1990.github.io/kardinal-promoter/troubleshooting/#start-here-kardinal-doctor"

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
          runbook_url: "https://pnz1990.github.io/kardinal-promoter/troubleshooting/#promotion-is-stuck"

      - alert: KardinalWorkQueueBacklog
        expr: workqueue_depth{name=~"bundle|promotionstep|policygate"} > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "kardinal work queue depth > 100 for {{ $labels.name }}"
          runbook_url: "https://pnz1990.github.io/kardinal-promoter/troubleshooting/#graph-controller-issues"
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
helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --set grafanaDashboard.enabled=true \
  --set 'grafanaDashboard.sidecarLabel.grafana_dashboard=1'
```

Grafana will pick up the dashboard automatically within 60 seconds. Search for **"kardinal-promoter"** in the Grafana dashboard list.

### Option 2 — Manual import

Download the dashboard JSON from the repository:

```
config/monitoring/kardinal-promoter-dashboard.json
```

In Grafana: **Dashboards → Import → Upload JSON file**.

The dashboard UID is `kardinal-promoter-v1`. Importing a second time will overwrite the existing dashboard.

With either option, the panels query the Prometheus datasource chosen in the
dashboard's **Prometheus** selector, which starts at your default Prometheus
datasource.

---

## Tracing (OpenTelemetry)

The controller can export OpenTelemetry traces over OTLP/HTTP to any collector or backend
that accepts it (the OpenTelemetry Collector, Jaeger, Tempo, Honeycomb, Datadog Agent, ...).
Tracing is off by default.

```yaml
tracing:
  enabled: true
  endpoint: http://otel-collector.observability:4318   # /v1/traces is added
  samplingRatio: 0.1                                   # default
```

| Value | Flag | Meaning |
|-------|------|---------|
| `tracing.enabled` | `--tracing-enabled` | Export traces. Default `false` |
| `tracing.endpoint` | `--tracing-endpoint` | An `http://` or `https://` URL, or `host:port`. Empty uses `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT` (set them with `controller.extraEnv`), else `localhost:4318` |
| `tracing.insecure` | `--tracing-insecure` | Plain HTTP to a `host:port` endpoint. A URL's scheme decides by itself |
| `tracing.samplingRatio` | `--tracing-sampling-ratio` | Fraction of new traces recorded, 0 to 1. A request that carries a sampled `traceparent` is always recorded |

Only OTLP over HTTP (protobuf, port 4318) is supported, not OTLP/gRPC. The standard
`OTEL_EXPORTER_OTLP_*` variables for headers, certificates and timeouts apply, through
`controller.extraEnv`. Spans have the resource `service.name=kardinal-controller` and
`service.version` set to the controller version.

| Span | Kind | Attributes |
|------|------|------------|
| `<controller>.Reconcile` (`bundle`, `promotionstep`, `policygate`, `notificationhook`, ...) | internal | `kardinal.controller`, `k8s.namespace.name`, `kardinal.object.name`, `kardinal.requeue_after_ms`; error status when the reconcile fails |
| `step <name>` (`step git-clone`, `step open-pr`, ...) | internal | `kardinal.step`, `kardinal.step.index`, `kardinal.environment`, `kardinal.step.status` |
| `git clone`, `git push` | internal | `server.address`, `kardinal.git.branch` (or `kardinal.git.commit`), `kardinal.git.force` |
| `HTTP <method>` | client | `http.request.method`, `server.address`, `url.scheme`, `http.response.status_code`: SCM API requests and NotificationHook deliveries |
| `webhook.scm`, `bundleapi.create` | server | `http.request.method`, `http.response.status_code`: inbound SCM webhooks and Bundle API calls |

**Trace context.** NotificationHook deliveries carry the W3C `traceparent` (and `tracestate`,
`baggage`) of their client span, so a receiver that traces can join the trace. SCM API
requests do not carry trace headers. `/webhook/scm` and `/api/v1/bundles` are reached before
the caller is authenticated, so an inbound `traceparent` is not trusted: their server span
starts a new trace, sampled by the controller's own sampler, with a link to the caller's span
(a CI job that traces sees the link, not a child).

**What spans never hold.** No URL path, query or user info in attributes, no headers, no
request or response bodies: incoming-webhook URLs and git remotes can carry tokens. A span
names only the host it talked to. When a span records an error, every URL in the error text
is cut to its scheme and host (`https://github.com/…`).

**Shutdown.** Buffered spans are exported every 5 seconds and once more when the controller
stops, after every reconciler and HTTP server has drained (at most 5 seconds more).

## Changing the Metrics Port

Set `metricsBindAddress` in Helm values to use a different port. The container port and
the NetworkPolicy follow it; `service.metricsPort` sets the port of the `kardinal-promoter`
Service, which targets the container port by name:

```yaml
# values.yaml
metricsBindAddress: ":9090"
service:
  metricsPort: 9090
```

---

## Further Reading

- [Security Guide](security.md) — network policy, RBAC
- [`kardinal metrics`](../reference/cli/kardinal-metrics.md) — DORA command reference
- [Troubleshooting](../troubleshooting.md) — debugging stuck promotions
