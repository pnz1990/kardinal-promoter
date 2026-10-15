# Metrics

The controller serves Prometheus metrics on its metrics port. [Monitor](../guides/monitoring.md)
shows how to scrape them, the sample queries, the alerts and the Grafana dashboard.

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

**Controller labels**: `bundle`, `bundle-retire`, `changewindow`, `clusterscmprovider`, `graphcleanup`, `hookrun`, `imageverification`, `metriccheck`, `notificationhook`, `pipeline`, `policygate`, `promotionstep`, `prstatus`, `renderrun`, `rollbackpolicy`, `scheduleclock`, `scmprovider`, `subscription`

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

The controller registers these on the same `/metrics` endpoint (`pkg/reconciler/observability/metrics.go`,
`pkg/accesslog`, `pkg/audit` and `pkg/reconciler/kubeevent`):

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kardinal_bundles_total` | Counter | `phase` | Bundle phase transitions, labelled by the phase entered |
| `kardinal_steps_total` | Counter | `type` (always `PromotionStep`), `result` (`succeeded`, `failed`) | PromotionSteps reaching a terminal state |
| `kardinal_gate_evaluations_total` | Counter | `result` (`allowed`, `blocked`) | PolicyGate evaluations |
| `kardinal_api_access_log_dropped_total` | Counter | `kind` (`denied`, `request`) | API access log lines not written over their per-second budget ([API access log](../guides/security.md#api-access-log)) |
| `kardinal_audit_write_failures_total` | Counter | `kind` (`Bundle`, `PromotionStep`, `PolicyGate`, `Pipeline`) | AuditEvent creates that failed and stay in the writer's `status.pendingAuditEvents` to be retried ([Audit outbox](../guides/security.md#audit-outbox)). While a finished Bundle's step or gate holds unwritten records, its Graph is not retired: the Bundle's `GraphRetired` condition stays `Unknown` (reason `WaitingForSteps`) and names the object, so a counter that keeps rising with Bundles stuck there means the outbox cannot drain |
| `kardinal_audit_events_dropped_total` | Counter | `kind`, `reason` (`overflow`, `invalid`) | Audit records never written: the outbox of 32 was full, or the API server rejected the record. Alert on any increase |
| `kardinal_pr_duration_seconds` | Histogram | — | Time from the PR opening (the `open-pr` step completing) to the merge the controller sees, observed once, when the step's move to `HealthChecking` is written |
| `kardinal_step_duration_seconds` | Histogram | `step` (step name, e.g. `git-clone`) | Duration of each promotion step, observed once, when the status that records it Completed or Failed is written. `wait-for-merge` lasts until the merge; `health-check` covers the health check and the bake |
| `kardinal_gate_blocking_duration_seconds` | Histogram | — | How long a PolicyGate was blocked before it allowed |
| `kardinal_promotionstep_age_seconds` | Histogram | — | PromotionStep age when it reaches a terminal state |
| `kardinal_auditevents_pruned_total` | Counter | — | AuditEvents deleted by retention (`audit.retention`) |
| `kardinal_events_dropped_total` | Counter | `recorder`, `type` | Kubernetes Events not written because the controller was over its rate: `type="Normal"` over `--event-qps`/`--event-burst`, `type="Warning"` over a namespace's `--event-warning-qps`/`--event-warning-burst` ([Kubernetes Events](../operate.md#kubernetes-events) lists each Warning's durable record) |
| `kardinal_pr_cleanup_failures_total` | Counter | `reason` | Deleted PromotionSteps whose PR the controller left open (`PRLeftOpen`) or could not close (`ClosePRFailed`): close those PRs by hand, since merging one changes an environment with no step tracking it ([Troubleshooting](../troubleshooting.md)) |
| `kardinal_hold_bundle_missing_total` | Counter | `pipeline_namespace`, `pipeline` | Holds whose rollback Bundle has been missing for `--hold-bundle-grace`, at most once per hold (the `HoldBundleMissing` AuditEvent is the durable record). The hold stays in effect until released ([A hold whose rollback Bundle does not exist](../rollback.md#a-hold-whose-rollback-bundle-does-not-exist)) |
| `kardinal_notifications_dropped_total` | Counter | `hook_namespace`, `hook`, `reason` (`template`, `attempts`) | Events a NotificationHook gave up on without delivering: the body could not be rendered for the event, or the last of 10 delivery attempts failed. Each also emits a `Warning` Event on the hook ([Templated body](../notifications.md#templated-body)) |

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
| `kardinal_git_transfer_bytes_total` | Counter | `git_service` (`fetch`, `push`), `direction` (`sent`, `received`) | Bytes git transferred over HTTP(S). `fetch` covers clones and fetches (git-upload-pack), `push` covers git-receive-pack. Git over ssh is not counted |

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
