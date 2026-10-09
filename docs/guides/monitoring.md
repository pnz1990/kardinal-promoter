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
release namespace. It collects every metric on this page, the SCM API and git metrics
included; no extra endpoint or relabeling is needed. Prometheus Operator sets the `job` label to the Service name, which
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
| `kardinal_api_access_log_dropped_total` | Counter | `kind` (`denied`, `request`) | API access log lines not written over their per-second budget ([API access log](security.md#api-access-log)) |
| `kardinal_pr_duration_seconds` | Histogram | — | Time from the PR opening (the `open-pr` step completing) to the merge the controller sees, observed once, when the step's move to `HealthChecking` is written |
| `kardinal_step_duration_seconds` | Histogram | `step` (step name, e.g. `git-clone`) | Duration of each promotion step, observed once, when the status that records it Completed or Failed is written. `wait-for-merge` lasts until the merge; `health-check` covers the health check and the bake |
| `kardinal_gate_blocking_duration_seconds` | Histogram | — | How long a PolicyGate was blocked before it allowed |
| `kardinal_promotionstep_age_seconds` | Histogram | — | PromotionStep age when it reaches a terminal state |

### SCM API and git metrics

Every call the controller makes to the SCM API (GitHub, GitLab, Forgejo/Gitea, Bitbucket,
Azure DevOps) and every git clone and push is measured (`pkg/scm/metrics.go`):

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kardinal_scm_requests_total` | Counter | `provider`, `owner`, `operation`, `result` | SCM API calls. `result` is `ok`, `client_error` (4xx), `server_error` (5xx), `rate_limited` (quota used up: 429, or 403 with `Retry-After` or no remaining quota), `network_error` (no response) or `circuit_open` (refused by the circuit breaker before any call) |
| `kardinal_scm_request_duration_seconds` | Histogram | `provider`, `operation` | Latency of the calls that were made, including reading the response |
| `kardinal_scm_rate_limit_remaining` | Gauge | `provider`, `owner` | What is left in the rate-limit window, from the last response's `X-RateLimit-Remaining` or `RateLimit-Remaining` header. Absent until a response carries one (see below) |
| `kardinal_scm_rate_limit_limit` | Gauge | `provider`, `owner` | The window's size (`X-RateLimit-Limit` / `RateLimit-Limit`) |
| `kardinal_scm_rate_limit_reset_timestamp_seconds` | Gauge | `provider`, `owner` | When the window resets, as a Unix time (`X-RateLimit-Reset` / `RateLimit-Reset`) |
| `kardinal_scm_circuit_state` | Gauge | `provider`, `owner` | Circuit breaker state: `0` closed, `1` half-open (one probe allowed), `2` open (calls refused). `owner="_quota"` is the circuit that opens when the token's rate limit is used up |
| `kardinal_git_operations_total` | Counter | `operation` (`clone`, `push`), `result` (`ok`, `error`, `non_fast_forward`) | Git clones and pushes. `non_fast_forward` is a push that lost to another writer of the branch (it is rebased and retried) |
| `kardinal_git_operation_duration_seconds` | Histogram | `operation` | Duration of git clones and pushes |
| `kardinal_git_transfer_bytes_total` | Counter | `service` (`fetch`, `push`), `direction` (`sent`, `received`) | Bytes git transferred over HTTP(S). `fetch` covers clones and fetches (git-upload-pack), `push` covers git-receive-pack. Git over ssh is not counted |

**Cardinality is bounded.** The `owner` label is the repository owner (organization, user,
top-level GitLab group, Bitbucket workspace or Azure DevOps organization). It is never the
repository. An owner gets its own value only after a call for it succeeded (a 2xx response), and
only the first 50 such owners do; calls for any other owner, including ones that only ever fail,
are reported as `_other`. Calls without an owner, such as the startup token check, use `_none`.
`operation` is the HTTP method and the API resource words of the path, for example
`POST pulls`, `GET merge_requests` or `DELETE git refs heads`. Names, numbers and SHAs are
dropped, and there are at most 100 values. Owner and operation values are cut to 64 bytes. The
reserved values `_other`, `_none` and `_quota` start with `_`. A controller has at most
5 providers × 52 owner values of each owner-labelled series.

**What the rate-limit gauges mean depends on the provider and the credential:**

- **GitHub** sends `X-RateLimit-*` on every response. With a personal access token the quota
  belongs to the token, not to the owner: every owner's gauge shows the same window, and it is
  per owner only in name. With a GitHub App, each installation (and so each organization) has its
  own window, which is why the gauges carry `owner`.
- **GitLab** sends `RateLimit-*` when rate limiting is enabled on the instance (it is on
  gitlab.com). The limit applies to the token's user.
- **Gitea and Forgejo** send no rate-limit headers by default (the API is not rate limited unless
  a proxy in front of it is), so the gauges are absent.
- **Azure DevOps** sends `X-RateLimit-*` only when a request is delayed or close to the limit, and
  counts in throughput units (TSTUs) over a sliding window, not in requests. Read
  `remaining` as usage units left, not calls left.
- **Bitbucket** sends none of these headers.

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

### SCM rate limit nearly used up

```promql
# Below 10% of the window left, for any owner
min by (provider, owner) (kardinal_scm_rate_limit_remaining / (kardinal_scm_rate_limit_limit > 0)) < 0.1
```

### SCM calls refused by an open circuit

```promql
sum by (provider, owner) (rate(kardinal_scm_requests_total{result="circuit_open"}[5m])) > 0
```

### Git push contention

```promql
# Share of pushes that lost to another writer of the branch
sum(rate(kardinal_git_operations_total{operation="push",result="non_fast_forward"}[15m]))
  / sum(rate(kardinal_git_operations_total{operation="push"}[15m]))
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
`stale_prod_days`, over the last 30 Bundles Verified in the last environment, and
`change_failure_rate` and `time_to_restore` over the last 30 deployments to it (see
[Change failure rate and time to restore](#change-failure-rate-and-time-to-restore)).

### Change failure rate and time to restore

The PipelineReconciler computes the two DORA stability metrics from PromotionStep and Bundle
status into `Pipeline.status.deploymentMetrics`, over the Pipeline's final environments: every
environment nothing depends on (one for a chain, several for a fan-out such as `prod-eu` and
`prod-us`):

| Field | Meaning |
|-------|---------|
| `deployments` | Deployments in the sample: the last 30 Bundles whose change reached a final environment (the step's `health-check` entry in `status.steps` started: after `git-push`, or after the merge for `pr-review`). A Bundle counts once, however many final environments and regions it reaches. Not deployments: a failure before that (a refused push, a closed PR), a promotion with nothing to change (`outputs.noChanges`), the steps a newer Bundle's supersession cancelled (a step that had already failed, is `AbortedByAlarm`, or is `RollingBack` when an automatic rollback supersedes its Bundle, still counts as a failed deployment), and rollback Bundles (they count only as restores) |
| `failedDeployments` | Deployments that failed: a step in any final environment ended `Failed`, `AbortedByAlarm` or `RollingBack` after its health check started, a rollback Bundle for a final environment later rolled back from it (`kardinal rollback`, the UI, RollbackPolicy or `onHealthFailure: rollback`; annotation `kardinal.io/rollback-from`), or the Bundle was rejected after it was deployed |
| `changeFailureRateMillis` | `failedDeployments / deployments` in thousandths (`250` = 25%) |
| `meanTimeToRestoreMinutes` | Mean whole minutes from each failed deployment reaching a final environment (its health check started: when users got the change) to the first later Bundle Verified in every final environment it targets: all of them, or, for a rollback of one environment, that one. Every one of its steps there (all regions) must be Verified; the last gives the time. A region of the failed Bundle itself, or an older Bundle, never restores it |
| `restoredFailures` | Failures counted in `meanTimeToRestoreMinutes`; one not restored yet is left out |

`deploymentMetrics` is set once a Bundle has been Verified in the last environment, or a
deployment there has failed. While every deployment has failed, it holds only the stability
fields (a 100% change failure rate); the lead-time and staleness fields stay unset, and
`kardinal metrics` prints `-` for them.

### Step timings

Every PromotionStep records each step of its sequence in `status.steps[]`: `name`, `state`,
`startedAt`, `completedAt` and `durationMs`. `wait-for-merge` spans the time the PR waited,
and `health-check` the real health check (bake included). The UI shows each step's duration
in the node detail panel, `kardinal logs` prints them, and `kardinal_step_duration_seconds`
exports them to Prometheus.

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
- **SCM API and git**: SCM requests by result, P99 latency by operation, rate limit left per owner, circuit breaker state, git clone and push P90 duration, git bytes transferred, and git operations by result
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
| `tracing.samplingRatio` | `--tracing-sampling-ratio` | Fraction of traces recorded, 0 to 1, decided at each trace's root (a reconcile or an inbound request); a span's children follow it. An inbound `traceparent` is linked, not trusted, so it does not force recording |

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
