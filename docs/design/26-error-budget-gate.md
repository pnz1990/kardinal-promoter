# Design: Error-budget gate

> Status: Proposed (spec for v0.14.0, AGENTIC-PLAN §5 "Error-budget gate"). Implementation waits
> until v0.13.0 is tagged and follows in separate PRs (§19).
> Created: 2026-10-15. Audited 2026-10-21 against what v0.13 ships (main `95f53aec0`): every
> `path:line` re-checked, the Sloth and Pyrra facts re-read from their sources, the queries changed
> where those facts broke them (§5), a gaming section added (§15.1), the defaults stated (§16.1),
> and the test plan moved onto the suites that exist (§18).
> Depends on: [18-actor-policy.md](18-actor-policy.md) (`actor.*`, `approval.requiredWhen`, freezes
> and stop-the-line, §9; all on main), the existing MetricCheck and PolicyGate reconcilers, and the
> `<release>-risk-owners` admission policy of [25-change-risk.md](25-change-risk.md) §11, which this
> spec extends (§11; whichever spec lands first creates it), and #2446 (only the controller writes
> kardinal's status subresources, PolicyGates' and MetricChecks' among them), without which a namespace admin can forge the status a budget
> gate reads (§11).
> Verified against: this repository at `main` (`95f53aec0`); kro v0.10.0-rc.0 (`54a203b`, the pin of
> `hack/install-kro.sh`) at `/local/home/rrroizma/kro-upstream`; Sloth
> [v0.16.0](https://github.com/slok/sloth/tree/v0.16.0) and Pyrra
> [v0.10.2](https://github.com/pyrra-dev/pyrra/tree/v0.10.2), still the latest releases on
> 2026-10-21, read from their tagged sources; Prometheus v3.15.0 (the e2e suites' version) for
> PromQL semantics; the Google [SRE workbook](https://sre.google/workbook/table-of-contents/),
> chapter 5 "Alerting on SLOs" and appendix B "Example Error Budget Policy". Source references are
> `path:line` in those trees. Nothing here is implemented yet.
>
> **Defaults.** Budget evaluation is off by default: no MetricCheck reads a budget without
> `spec.errorBudget`, `slo.*` lists only such checks, the chart ships no SLO check, no gate, and
> neither Sloth nor Pyrra, and an unknown budget blocks only the gates that read it. **One rule is
> on by default:** §11's agent rule (chart value `admission.agentGateInputs`, default `refuse`),
> because the hole it closes exists for every gate, budgets or not. It changes v0.13's behaviour
> for requesters the AgentIndex names as agents (they may no longer write gate templates or
> hand-written MetricChecks) and, while the index is not Ready, for ServiceAccounts; it never
> refuses a person. **That includes GitOps controllers:** Argo CD's or Flux's ServiceAccount applying
> PolicyGates or MetricChecks is refused before the index's first build (a sync that applies the
> chart and its gate templates together retries), while the index is `IndexFull`, and in a
> `CompactFormat` window (an upgrade or rollback in progress). The remedy is to trim
> AgentIdentities, or set `allow`, which keeps v0.13's behaviour. It is still narrower than
> v0.13's `IndexFull`, which refuses everyone the actor policies check. One exception to "never
> refuses a person": with no AgentIndex at all, everyone is refused (§11, §16, §16.1).

---

## 1. Problem and scope

A team that runs on SLOs has an error budget policy. The workbook's example states it directly:
"If the service has exceeded its error budget for the preceding four-week window, we will halt all
changes and releases other than P0 issues or security fixes until the service is back within its
SLO" ([appendix B, SLO Miss Policy](https://sre.google/workbook/error-budget-policy/)). Today a
kardinal user can write that rule only as a hand-built PromQL MetricCheck per SLO, with no
burn-rate logic, no exception model and one number per check.

This spec adds:

| # | Piece | Section |
|---|-------|---------|
| B1 | A burn-rate preset on MetricCheck, `spec.errorBudget`, for Sloth, Pyrra or plain Prometheus recording rules | 4, 5 |
| B2 | Multiwindow, multi-burn-rate evaluation, as in the SRE workbook | 5.3 |
| B3 | `slo.<name>.*` in PolicyGate CEL | 6 |
| B4 | Staleness, and fail-open or fail-closed when Prometheus is down | 7 |
| B5 | Hotfix exceptions in CEL: label, actor, approval, override, rollback | 8 |
| B6 | How the gate interacts with freezes, stop-the-line, pause and holds | 9 |
| B7 | Per-environment and per-fleet-target SLO mapping | 10 |
| B8 | Agents may not change a gate's inputs (MetricChecks written by hand, gate templates) | 11, 15.1 |

### 1.1 Non-goals

- **kardinal does not define SLOs.** Sloth, Pyrra or the team's own recording rules do. kardinal
  reads the series they record.
- **No SLO CRD of kardinal's own.** Section 3, option (e).
- **No raw long-range queries.** The preset never sends `rate(...[30d])`; it reads recording rules
  only (section 5.2). A team without recording rules writes them first.
- **No recovery forecast** ("the budget recovers on Thursday"). It needs the error history over
  the window; out of scope.
- **No automatic freeze.** A spent budget never writes a freeze ChangeWindow (section 9).
- **No backends other than Prometheus** for the preset (Datadog SLOs, CloudWatch SLOs, Nobl9). The
  existing `datadog`, `cloudwatch` and `web` providers can read a budget as one number today.

## 2. Graph-first

| Concern | Mechanism | Graph-first question |
|---------|-----------|----------------------|
| Reading the budget and burn rates | The MetricCheck reconciler queries Prometheus and writes its own status (`status.errorBudget`) | (2) a reconciler that writes its own CRD status; the I/O is the MetricCheck query that exists today, rationed by the G8 row "MetricCheck query slots (`Limiter`, #1479)" |
| The freeze | A PolicyGate whose CEL reads `slo.<name>.*`, built from MetricCheck status by the PolicyGate reconciler, as `metrics.*` | (3) a CEL variable; it joins the G8 row "PolicyGate CEL (`bundle.*`, `schedule.*`, `metrics.*`, `upstream.*`)", which gains `slo.*` with the implementation (§19) |
| Per-promotion SLO checks (canary) | A `perPromotion` MetricCheck template with `errorBudget`, instantiated by the Graph as an owned node, as today (with the builder changes of 4.3) | (2) owned node |
| Hotfix exceptions | CEL in the gate expression and `approval.requiredWhen`; gate overrides | (3) CEL; existing override admission |
| Who may change a gate's inputs | An admission policy (§11) | Admission, not promotion logic |

**No Prometheus in the gate path.** A gate reads MetricCheck status from the manager's cache. The
only process that talks to Prometheus is the MetricCheck reconciler: one instant query per check per
interval, through the egress guard and the Limiter, written to the check's own status. A gate
evaluation makes no network call, and a gate decides staleness from the status's `validUntil` and
`lastKnownAt` with the PolicyGate reconciler's clock (7.2).

No new G8 row and no new reconciler. The one kro gap found while comparing options (an optional
`ref`) is recorded as [G25](16-graph-capability-ledger.md#g25-a-ref-cannot-be-optional); the
chosen design does not depend on it.

## 3. Where the budget comes from: options

### 3.1 What the SLO tools publish

The plan proposed a `ref` node on the SLO tools' CRs. That only works if their status carries the
budget. It does not, in either tool.

**Sloth v0.16.0.** `PrometheusServiceLevel` (`sloth.slok.dev/v1`, namespaced,
`pkg/kubernetes/api/sloth/v1/types.go:20`, `register.go:12`, `pkg/kubernetes/api/sloth/register.go:4`)
has this status (`types.go:220-234`):

| Field | Meaning |
|-------|---------|
| `promOpRulesGeneratedSLOs` | SLOs whose PrometheusRule was generated |
| `processedSLOs` | SLOs processed |
| `promOpRulesGenerated` | the rules were generated |
| `lastPromOpRulesSuccessfulGenerated` | when |
| `observedGeneration` | the generation acted on |

It says whether the rules exist, not what they measure. The budget is in Prometheus, as recording
rules that Sloth's metadata plugin writes for every SLO
(`internal/plugin/slo/core/metadata_rules_v1/plugin.go:73-115`, names in
`pkg/common/conventions/slo.go:8-29`):

| Series | Value |
|--------|-------|
| `slo:sli_error:ratio_rate<window>` | error ratio over `<window>`, for each alert window and the SLO period (`internal/plugin/slo/core/sli_rules_v1/plugin.go:58-60`) |
| `slo:objective:ratio`, `slo:error_budget:ratio` | objective, and `1 - objective` |
| `slo:time_period:days` | the SLO period |
| `slo:current_burn_rate:ratio`, `slo:period_burn_rate:ratio` | burn rate over the shortest page window, and over the period |
| `slo:period_error_budget_remaining:ratio` | `1 - slo:period_burn_rate:ratio`: the share of the budget left |

Every series carries `sloth_service`, `sloth_slo` and `sloth_id` (the label names at
`conventions/slo.go:21-23`, set by `GetSLOIDPromLabels`, `:34-40`). `sloth_id` is `<service>-<slo name>` (`internal/storage/io/k8s_sloth.go:139`):
it does not include the namespace, so two namespaces with the same service and SLO names record
colliding series unless their `spec.labels` differ.

Three facts about these series that the preset depends on:

- **Label sets differ.** The error-ratio series carry `sloth_window`, the SLO's labels and whatever
  labels the SLI query's output keeps (`sli_rules_v1/plugin.go:126-132`); the metadata series
  (objective, error budget, period) carry only the SLO's labels (`metadata_rules_v1/plugin.go:40`).
  Sloth's own burn-rate rule therefore divides with `/ on(sloth_id, sloth_slo, sloth_service)
  group_left` (`metadata_rules_v1/plugin.go:131-134`).
- **Window names are Prometheus durations.** The suffix is `model.Duration.String()`
  (`pkg/common/utils/prometheus/prometheus.go:11-13`): 24h is `1d` and 28d is `4w`, so a 28-day SLO
  records `slo:sli_error:ratio_rate4w`.
- **The optimized period rule can stay NaN.** By default Sloth records the period's error ratio as
  the average of the 5-minute ratios over the period (`sum_over_time(slo:sli_error:ratio_rate5m[30d])
  / ignoring(sloth_window) count_over_time(...)`, `sli_rules_v1/plugin.go:43-46,81-88,184-187`). A
  5-minute window with no requests records NaN (0/0), and `sum_over_time` over a range that holds a
  NaN is NaN, so for a service with one idle 5 minutes in the period the budget series is NaN for
  the whole period, including while the service burns its budget. The average is also
  time-weighted, not request-weighted. Sloth's `disableOptimized` setting for the SLI rules plugin
  records the period ratio from the raw counters instead. The preset treats a NaN budget as
  unknown (7.1), so a quiet service on optimized rules fails closed, not open; the docs recommend
  `disableOptimized` for every SLO a gate reads; it is an option of the SLI rules plugin, set per
  SLO through its `plugins.chain` (`sli_rules_v1/plugin.go:24-26`). That moves cost, it does not
  remove it: kardinal still sends no long-range query (1.1), but Sloth's own period rule then
  evaluates a raw rate over the whole period at every rule evaluation, in Prometheus. (The NaN propagation is PromQL
  semantics; the contract row METRIC-EB-07 confirms it on a real Sloth, and that the plugin option
  replaces the optimized rule, §18.)
- **The window catalog is the controller's.** The `PrometheusServiceLevel` CRD has no per-SLO
  period (`pkg/kubernetes/api/sloth/v1/types.go:55-92`): every SLO gets the controller's
  `defaultWindowPeriod` (`internal/storage/io/k8s_sloth.go:143`; `--default-slo-period`, 30d by
  default), and `--slo-period-windows-path` replaces the built-in window sets
  (`cmd/sloth/commands/k8scontroller.go:105-106`). The preset reads the period from
  `slo:time_period:days`, so it follows whatever the controller uses. The preset's defaults (5.2) assume
  the built-in catalog; a controller run with its own catalog needs the gate to name the same pairs
  in `windows`.

**Pyrra v0.10.2.** `ServiceLevelObjective` (`pyrra.dev/v1alpha1`,
`kubernetes/api/v1alpha1/groupversion_info.go:29`) has this status
(`kubernetes/api/v1alpha1/servicelevelobjective_types.go:243-247`):

```go
type ServiceLevelObjectiveStatus struct {
	// Type is the generated resource type, like PrometheusRule or ConfigMap
	Type string `json:"type,omitempty"`
}
```

Availability and budget exist in Pyrra, but in its **API server**, not in the CRD: the Connect/gRPC
`ObjectiveService.GetStatus` returns `ObjectiveStatus{availability{percentage,total,errors},
budget{total,remaining,max}}` (`proto/objectives/v1alpha1/objectives.proto:10-18,115-131`),
computed per request by querying Prometheus (`main.go:855`, `main.go:942-957`:
`remaining = (budget.total - errors/total) / budget.total`). An objective with no requests at all
is left out of the response (`main.go:947-950`), and a NaN `remaining` with requests is reported as
1 (`main.go:955-957`). In Prometheus, Pyrra records:

| Series | Value | When |
|--------|-------|------|
| `<metric>:burnrate<window>{slo="<name>"}` | the **error ratio** over `<window>` (not divided by the budget); its alerts compare it with `factor * (1 - target)` (`slo/rules.go:139-148`) | always; `<metric>` is the indicator's total metric without `_total`/`_count`, or the gauge metric for a boolGauge SLO (`slo/rules.go:431-449`) |
| `pyrra_objective`, `pyrra_window`, `pyrra_availability{slo="<name>"}` | target ratio, window in seconds, availability over the window (`slo/rules.go:1508-1512,1513-1517,1580-1584`) | only when the operator runs with `--generic-rules`, which defaults to `false` (`main.go:91`), and, for ratio, latency and boolGauge SLOs, only without `grouping`: `GenericRules` returns `ErrGroupingUnsupported` for those (`slo/rules.go:1521-1523`), but not for latencyNative ones (`slo/rules.go:1743-1783`), and the operator ignores that error (`kubernetes/controllers/servicelevelobjective.go:309-312`) |

The `slo` label is the SLO's name (`slo/rules.go:596-608`). The `pyrra_*` series carry only it and
the objective's labels with the `pyrra.dev/` prefix, recorded without the prefix (`pyrra.dev/team`
on the objective is `team` on the series, `slo/rules.go:601-605,1502`), and, with
`ruleOutput.enableDescriptionAsLabel`, a `description` label (`slo/rules.go:1504-1506`); the
`:burnrate` series also
carry every equality matcher of the indicator's selector (`slo/rules.go:77-82`). No series carries the
namespace (the objective holds it internally, `servicelevelobjective_types.go:614-619`), so two
namespaces with the same SLO name collide in a shared Prometheus, as Sloth's do.

Pyrra's windows are its period scaled and rounded to the minute (`slo/rules.go:1448-1475`): a 28-day
SLO records 5m, 30m, 1h, 2h, 6h, 1d and 4d; a 30-day SLO records 5m, 32m, 1h4m, 2h9m, 6h26m,
1d1h43m and 4d6h51m. The series names cannot be built without the period. Pyrra's ratio rule
`sum(rate(errors[w])) / sum(rate(total[w]))` has no `or vector(0)` (`slo/rules.go:454`; its
availability rule does, `slo/rules.go:1525`): a service whose error counter has never been
exported records no `:burnrate` series at all.

### 3.2 Options

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A `ref` node on the Sloth `PrometheusServiceLevel`; a gate or `readyWhen` reads its status | Graph-native; no query | The status has no budget (3.1): the gate could learn only that rules were generated. A missing SLO object holds every node that reads it (G25); a CRD not installed fails the whole Graph compile with a literal GVK (G4); RBAC on a third-party kind for every Graph identity (section 11) |
| (b) | A `ref` node on the Pyrra `ServiceLevelObjective` | Same | Same: the status is `{type}` only. Budget and availability come from Pyrra's API server, which a `ref` cannot call |
| (c) | **A burn-rate preset on MetricCheck** (`spec.errorBudget`): the MetricCheck reconciler reads the recorded series from Prometheus and writes budget, burn rates and alert state to its own status | Uses the series both tools actually publish, and plain recording rules for teams without either tool; the existing provider, egress guard, Limiter, staleness and per-promotion instances apply; no third-party RBAC | One more field on MetricCheck; the preset must know each tool's series names and window rules (versioned conventions, section 5.2) |
| (d) | The `web` provider against Pyrra's API (`POST http://<pyrra-api>:9099/objectives.v1alpha1.ObjectiveService/GetStatus`, JSONPath `{.status[0].budget.remaining}`; port from `main.go:468`) | No code | Pyrra only; one number per check (no burn rates); a second hop that queries Prometheus on each call. An objective with no requests is left out of the response, so `status[0]` selects nothing and the check fails; with grouping the statuses come in map order (`main.go:896,941`), so `status[0]` is an arbitrary group. The Connect JSON request shape is not verified against a running Pyrra |
| (e) | A new kardinal `ErrorBudget` CRD and reconciler | Clean status of its own | Duplicates MetricCheck (provider, egress, Limiter, staleness, instances, admission); a second way to gate on a metric |

**Choice: (c).** (a) and (b) fail on the facts: the CRs do not carry the budget. If a future Sloth
or Pyrra release writes budget into status, a `ref` could replace the query for that tool; the
cost (G4, G25, section 11) stays and is listed so nobody has to check it again. (d) remains
documented as a no-code recipe for Pyrra users who want only `budget.remaining`, with its caveats.

## 4. The preset

### 4.1 Examples

Sloth:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: MetricCheck
metadata:
  name: checkout-availability
  namespace: checkout
spec:
  provider: prometheus
  prometheusURL: http://prometheus.monitoring.svc:9090
  interval: 1m
  threshold: {operator: gt, value: 0}     # result Pass while budget is left (4.3)
  errorBudget:
    source: sloth
    sloth:
      service: checkout
      slo: requests-availability
    matchers: {team: payments}             # optional; a label every series of the SLO carries (5.2)
    # alertWindows: from the SLO's period (slo:time_period:days): google-30d or google-28d (5.2)
```

Pyrra (the operator must run with `--generic-rules`; the SLO must not use `grouping`, 3.1):

```yaml
  errorBudget:
    source: pyrra
    pyrra:
      slo: checkout-availability           # the ServiceLevelObjective name (the `slo` label)
      window: 4w                           # the SLO's spec.window: the burn-rate series names depend on it
      # objective: "0.999"                 # optional; checked against pyrra_objective
    alertWindows: pyrra                    # the default for pyrra: Pyrra's windows and factors for the window
```

Plain recording rules, named as in the workbook (`job:slo_errors_per_request:ratio_rate1h`):

```yaml
  errorBudget:
    source: prometheus
    prometheus:
      errorRatioMetric: "job:slo_errors_per_request:ratio_rate"   # the window is appended as written: ...rate5m
      objective: "0.999"
      window: 30d                          # needs a ...rate30d rule for the budget, or:
      # budgetRemainingMetric: job:slo_error_budget_remaining:ratio   # a series that holds the budget left
    matchers: {job: checkout}
```

### 4.2 Go types sketch

```go
// ErrorBudgetSpec makes a prometheus MetricCheck read an SLO's error budget and
// burn rates from recording rules instead of running spec.query.
// +kubebuilder:validation:XValidation:rule="(self.source == 'sloth') == has(self.sloth) && (self.source == 'pyrra') == has(self.pyrra) && (self.source == 'prometheus') == has(self.prometheus)",message="set exactly the block of errorBudget.source"
// +kubebuilder:validation:XValidation:rule="!(has(self.alertWindows) && has(self.windows))",message="set alertWindows or windows, not both"
// +kubebuilder:validation:XValidation:rule="!(self.source == 'pyrra' && has(self.groupBy))",message="the pyrra preset reads no grouped SLO (Pyrra records no budget for most of them): use source prometheus"
type ErrorBudgetSpec struct {
	// +kubebuilder:validation:Enum=sloth;pyrra;prometheus
	Source     string                 `json:"source"`
	Sloth      *SlothErrorBudget      `json:"sloth,omitempty"`
	Pyrra      *PyrraErrorBudget      `json:"pyrra,omitempty"`
	Prometheus *PrometheusErrorBudget `json:"prometheus,omitempty"`

	// Matchers are extra equality matchers added to every series read. Keys
	// are Prometheus label names that every series of the SLO carries (5.2);
	// values are escaped by the reconciler and may hold MetricCheck
	// placeholders (instances only, 4.3).
	// +kubebuilder:validation:MaxProperties=8
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[a-zA-Z_][a-zA-Z0-9_]*$'))",message="matcher keys must be Prometheus label names"
	Matchers map[string]string `json:"matchers,omitempty"`

	// AlertWindows names a window set: google-30d, google-28d or pyrra.
	// Default: pyrra for source pyrra; for sloth, the set of the SLO's period
	// (5.2); for prometheus, the set of spec.prometheus.window (30d or 28d,
	// else required).
	// +kubebuilder:validation:Enum=google-30d;google-28d;pyrra
	AlertWindows string `json:"alertWindows,omitempty"`
	// Windows replaces the set. At most 6 pairs.
	// +kubebuilder:validation:MaxItems=6
	Windows []BurnWindow `json:"windows,omitempty"`

	// GroupBy reads one SLO per value of a label, for fleets (section 10).
	GroupBy *ErrorBudgetGroupBy `json:"groupBy,omitempty"`
}

// Durations are Prometheus durations, parsed with model.ParseDuration
// (github.com/prometheus/common/model): 5m, 1h, 1d, 4w. Units in
// descending order, each at most once, as ParseDuration requires.
// +kubebuilder:validation:Pattern=`^([0-9]+y)?([0-9]+w)?([0-9]+d)?([0-9]+h)?([0-9]+m)?([0-9]+s)?([0-9]+ms)?$`
// +kubebuilder:validation:MinLength=2
type PromDuration string

// +kubebuilder:validation:XValidation:rule="has(self.budgetPercent) != has(self.factor)",message="set budgetPercent or factor"
type BurnWindow struct {
	// +kubebuilder:validation:Enum=page;ticket
	Severity string `json:"severity"`
	// BudgetPercent is the share of the period's budget this pair detects
	// (the workbook's "error budget consumed"), e.g. "2". The factor is
	// computed from it with the SLO's period (section 5.3).
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	BudgetPercent string `json:"budgetPercent,omitempty"`
	// Factor is the burn-rate threshold itself, e.g. "14".
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	Factor string       `json:"factor,omitempty"`
	Short  PromDuration `json:"short"` // "5m"; shorter than long (checked by the reconciler)
	Long   PromDuration `json:"long"`  // "1h"
}

type SlothErrorBudget struct {
	Service string `json:"service"` // sloth_service
	SLO     string `json:"slo"`     // sloth_slo
}

type PyrraErrorBudget struct {
	SLO string `json:"slo"` // the slo label
	// Window is the SLO's spec.window. Required: Pyrra's window names are
	// computed from it (3.1); checked against pyrra_window.
	Window PromDuration `json:"window"`
	// Objective, when set, is checked against pyrra_objective.
	// +kubebuilder:validation:Pattern=`^0\.[0-9]*[1-9][0-9]*$`
	Objective string `json:"objective,omitempty"`
}

type PrometheusErrorBudget struct {
	ErrorRatioMetric string `json:"errorRatioMetric"`
	// +kubebuilder:validation:Pattern=`^0\.[0-9]*[1-9][0-9]*$`
	Objective             string       `json:"objective"`
	Window                PromDuration `json:"window"`
	BudgetRemainingMetric string       `json:"budgetRemainingMetric,omitempty"`
}

type ErrorBudgetGroupBy struct {
	Label string `json:"label"` // a Prometheus label, e.g. cluster
	// +kubebuilder:validation:Enum=targetName;targetLabel
	Match       string `json:"match,omitempty"`       // default targetName
	TargetLabel string `json:"targetLabel,omitempty"` // the fleet target label key when match is targetLabel
}

// In MetricCheckStatus:
type ErrorBudgetStatus struct {
	Objective       string           `json:"objective,omitempty"`       // "0.999"
	Window          string           `json:"window,omitempty"`          // "30d": the SLO's period
	WindowSet       string           `json:"windowSet,omitempty"`       // google-30d | google-28d | pyrra | custom
	BudgetRemaining string           `json:"budgetRemaining,omitempty"` // "0.412"; negative when overspent; the lowest group with groupBy
	BurnRates       []BurnRateSample `json:"burnRates,omitempty"`       // +listType=map +listMapKey=window
	Burning         string           `json:"burning,omitempty"`         // none | ticket | page
	NoTraffic       bool             `json:"noTraffic,omitempty"`
	LastKnownAt     *metav1.Time     `json:"lastKnownAt,omitempty"`     // the last evaluation with every value (6)
	SpecHash        string           `json:"specHash,omitempty"`        // hash of spec.errorBudget these values are for (4.3)
	Groups          []ErrorBudgetGroup `json:"groups,omitempty"`        // +kubebuilder:validation:MaxItems=512; more groups fail the evaluation
}

type BurnRateSample struct {
	Window string `json:"window"`
	Value  string `json:"value"` // "3.21"
}

type ErrorBudgetGroup struct {
	Key             string           `json:"key"` // the groupBy label's value
	BudgetRemaining string           `json:"budgetRemaining,omitempty"`
	BurnRates       []BurnRateSample `json:"burnRates,omitempty"`
	Burning         string           `json:"burning,omitempty"`
	NoTraffic       bool             `json:"noTraffic,omitempty"`
}
```

Numbers in status are strings, as `status.lastValue` is. Values are rounded: budget to 3 decimals,
burn rates to 2 (section 13, gate wake-ups). Decisions use the unrounded values: `threshold` and
`burning` are computed before rounding. A query that returns more than 512 groups fails the
evaluation (`groupBy matches N groups, at most 512`); groups are never truncated, since the lowest
budget would then be wrong.

### 4.3 What stays as it is, and what changes

- **`status.lastValue` and `status.result`.** `lastValue` is the budget remaining; `threshold`
  applies to it, so `metrics.<name>.result == "Pass"` keeps working and means "budget left" with
  `{operator: gt, value: 0}`. `lastValue` holds the budget rounded to 3 decimals, since the gate
  watch wakes on every `lastValue` change (`policygate/reconciler.go:1339`); `threshold.value` is a
  `float64` (`metriccheck_types.go:299`) and compares the unrounded budget, so a budget of 0.0004
  passes `gt 0` though status shows `0.000`.
  `threshold` stays required: the schema does not change for existing checks. With `groupBy`,
  `lastValue` and `status.errorBudget.budgetRemaining` are the lowest budget of any group. While the
  budget is unknown (7.1), `lastValue` is cleared and `result` is `Fail`.
- **`status.validUntil`** (three intervals, at least 30 s;
  `pkg/reconciler/metriccheck/reconciler.go:61-65`, set at `:515-517`) covers the new fields.
- **Spec edits.** `status.errorBudget.specHash` records the `spec.errorBudget` its values are for
  (the reconciler keeps nothing between reconciles, and every edit bumps `generation`, so status
  must say which spec produced it). When the hash differs, the status is cleared: another SLO's
  history is not this one's. An edit to `prometheusURL` alone keeps `lastKnownAt`, so a bounded
  fail-open (7.2) counts from the last known value, not from the edit.
- **`perPromotion` and `suspend`** apply (`api/v1alpha1/metriccheck_types.go:78-99`), with four
  changes to the Graph builder (`pkg/graph/metriccheck.go`) that per-promotion presets need:
  - **Instance creation.** An instance is created only for a gate whose expression reads the check
    (`ExprReadsMetric`, `:126-140`, called at `:176`), which matches `metrics.<name>` or the quoted
    name: `slo["x"]` matches by accident, `slo.x` not at all, and `approval.requiredWhen` is not
    read. It learns `slo.<name>` and reads `requiredWhen` too.
  - **Placeholders.** `metricCheckSpec` (`:249-280`) renders only `query` and the `web` fields, and
    the reconciler refuses leftover placeholders only there (`templatedTexts`,
    `metriccheck/reconciler.go:296-307`). Both gain the values of `errorBudget.matchers`, so a
    preset with `matchers: {version: "{{ bundle.version }}"}` measures the new version's own SLI,
    for canaries.
  - **The upstream hold.** The instance is held until its upstreams are Verified by a
    `resolvableWhen` on the `query` field (`:205-213`, the field chosen at `:274-277`). A preset has
    no `query`, so the hold moves to `errorBudget.source`.
  - **Exclusivity.** `query` and `errorBudget` are exclusive, and the CRD rule tolerates the empty
    `query` of a preset (next point).
- **Validation.** `errorBudget` requires `provider: prometheus`. The rule "query is required for every
  provider except web" (`metriccheck_types.go:18`) becomes "... except web, and except prometheus
  with errorBudget"; `query` and `errorBudget` are exclusive, and so are `threshold.text` and
  `errorBudget` (a budget is a number). Label names match
  `^[a-zA-Z_][a-zA-Z0-9_]*$` (map keys through `self.all(k, k.matches(...))`, since an OpenAPI
  pattern cannot check keys); metric names `^[a-zA-Z_:][a-zA-Z0-9_:]*$`; `objective` is a ratio in
  (0, 1), checked by a pattern; windows are Prometheus durations (`PromDuration`, 4.2), not the
  interval pattern (`metriccheck_types.go:74`), which allows no `d` or `w` and would refuse `30d`.
  A read objective is compared with a given one to within 1e-9: Pyrra computes `target / 100`
  (`kubernetes/api/v1alpha1/servicelevelobjective_types.go:643`, parsed at `:414`; `slo/rules.go:1510`
  only formats it), so 99.9 is 0.9990000000000001.

## 5. Queries

### 5.1 One request per evaluation

The reconciler builds one PromQL instant query that returns every **raw** value it needs, tagged
with a `kardinal_series` label through `label_replace`, joined with `or`:

```
label_replace(<budget>, "kardinal_series", "budget", "", "")
or label_replace(<objective>, "kardinal_series", "objective", "", "")
or label_replace(<period>, "kardinal_series", "period", "", "")
or label_replace(<ratio 5m>, "kardinal_series", "ratio5m", "", "")
or label_replace(<ratio 1h>, "kardinal_series", "ratio1h", "", "")
or ...
```

`or` keeps every element because the `kardinal_series` values differ (Prometheus matches `or` on
every label but `__name__`), and `label_replace` with an empty regex keeps the series' name and
labels. **Burn rates are divided in Go, not in PromQL** (5.3): the series do not share label sets
(3.1), so a division would need `on(...) group_left` per source, and with two matching SLOs it
either returns two quotients nobody can tell apart from groups or, with a duplicate on the "one"
side, fails the whole query (`found duplicate series for the match group`) instead of naming them.

One request means one Limiter slot per SLO per interval, as a plain MetricCheck costs today. The
provider gains a vector reader next to `extractScalar` (`pkg/reconciler/metriccheck/prometheus.go:185-230`):
without `groupBy`, exactly one element per `kardinal_series` value, else the evaluation fails with
`selector matches N SLOs` (the existing one-series rule).

**Grouped checks** (§10) aggregate the per-group values by the group label so the response stays
small: `max by (<label>, kardinal_series) (...)` for the budget and each ratio, plus
`count by (<label>) (<budget>)` tagged `budget_count`, so a group whose budget comes from two SLOs is
reported (`selector matches 2 SLOs in group eu-west`). The objective and the period come from
metadata series without the group label; they are read unaggregated and must be exactly one element
each, as without `groupBy`, since a `max` would fold two SLOs' objectives into one (a longer period
raises the factors, so the gate would burn later). They apply to every group.

The read of a plain check is capped at 1 MiB (`maxResponseBytes`, `prometheus.go:25,135`). An
element of the aggregated response is at most about 160 bytes with a 63-character group key (a
fleet target name or a label value, both at most 63 characters), a short tag (`r1d1h43m`) and a
17-digit value, so 512 groups with a budget, a count and 12 windows are up to about 1.2 MiB. A
grouped preset check therefore reads up to 2 MiB; a larger response, from more or longer group keys
than a fleet maps, fails as a query error (`response over 2 MiB: narrow the matchers`). Without the
aggregation each element keeps every source label (about 250 bytes). A unit test builds the largest
response with 63-character keys and 17-digit values (§18).

### 5.2 Per source

With `M` the matchers (the source's own labels plus `spec.errorBudget.matchers`, escaped) and `<w>`
a window's name (below):

| Value | sloth | pyrra | prometheus |
|-------|-------|-------|------------|
| error ratio at `w` | `slo:sli_error:ratio_rate<w>{M}` | `{__name__=~".+:burnrate<w>", M}` | `<errorRatioMetric><w>{M}` |
| objective | `slo:objective:ratio{M}` | `pyrra_objective{M}`, checked against `objective` when given | `objective` (spec) |
| period | `slo:time_period:days{M}` | `pyrra_window{M}`, checked against `pyrra.window` | `window` (spec) |
| budget remaining | `slo:period_error_budget_remaining:ratio{M}` | `(a - o) / (1 - o)` in Go, from `pyrra_availability{M}` and the objective | `budgetRemainingMetric{M}`, else `1 - r / (1 - o)` in Go, with `r` the ratio at `window` |

The burn rate at `w`, for every source, is the error ratio at `w` divided by `1 - objective`, in Go
(Sloth's own rule divides by `slo:error_budget:ratio`, which is `1 - objective`,
`metadata_rules_v1/plugin.go:131-134`).

**Window names.** For sloth and pyrra, `<w>` is the window's Prometheus duration string
(`model.Duration(w).String()`, as both tools name series: Sloth
`pkg/common/utils/prometheus/prometheus.go:11-13`, Pyrra `slo/rules.go:448`): `1d`, not `24h`; `4w`,
not `28d`. For `source: prometheus` the suffix is the window exactly as written, since the team named
the rules.

Constraints, stated so nobody relies on more:

- **Only recorded windows.** Sloth records error ratios only for its alert windows and the period
  (`sli_rules_v1/plugin.go:58-60`); Pyrra only for its windows, the period scaled and rounded to the
  minute (3.1). A window the tool does not record returns no series: No data, fail closed (7.1).
- **The default set is the tool's own for the SLO's period.** For sloth, the query reads
  `slo:time_period:days` and the reconciler uses `google-30d` for 30 and `google-28d` for 28:
  Sloth's `internal/alert/windows/google-30d.yaml` and `google-28d.yaml`, which Sloth itself picks by
  the SLO's period (`internal/alert/alert.go:35`, `GetWindows`, `window.go:224-231`). Both name the
  same windows (pages 5m/1h and 30m/6h, tickets 2h/1d and 6h/3d), so the query does not depend on
  the period, only the factors do. For any other period, `alertWindows` or `windows` is required
  (`period 7d has no default window set: set alertWindows or windows`). For pyrra, the windows are
  computed from `pyrra.window` with Pyrra's formula (`slo/rules.go:1448-1475`), which is why
  `window` is required: the names cannot be built without it, and reading `pyrra_window` first
  would be a second request. A Sloth set on a Pyrra SLO asks for windows Pyrra does not record.
- **Absent can mean "no errors yet".** Pyrra's ratio rule has no `or vector(0)` (3.1), so a service
  whose error counter has never been exported records no `:burnrate` series and fails closed as No
  data; Sloth event SLIs behave the same unless the SLI query adds `or vector(0)`. The reason says
  so (7.1), and the docs tell teams to initialize error counters to 0.
- **Matchers must be on every series.** `matchers` are added to every selector, so they must name
  labels every series of the SLO carries: Sloth's `spec.labels`, Pyrra's propagated labels (the
  objective's `pyrra.dev/` labels, without the prefix, 3.1), or
  labels a global query layer adds to every series (a `cluster` label from Thanos or Mimir). A label
  only the SLI query's output keeps (`sum by (cluster)`) matches the ratio series but not the
  objective, which is then missing: No data. The same holds for `groupBy.label`, except that the
  objective and period may lack it (5.1).
- **Pyrra's `__name__` regex** finds the burn-rate series without naming the indicator metric. A
  second recording rule with the same suffix and `slo` label would make two elements:
  `selector matches 2 SLOs`, fail closed.
- **Namespaces.** Sloth's `sloth_id` ignores the namespace, and Pyrra's series carry none (3.1). In
  a shared Prometheus, set `matchers` to a label that tells the SLOs apart (one of Sloth's
  `spec.labels`, a propagated Pyrra label).
- **Series names and window rules are tool conventions,** not APIs. They are pinned per source in
  one Go file with the tool version they were read from, and a live contract row runs the real
  controllers (METRIC-EB-07, §18).

### 5.3 Multiwindow, multi-burn-rate

From the SRE workbook, chapter 5,
[§6 Multiwindow, Multi-Burn-Rate Alerts](https://sre.google/workbook/alerting-on-slos/#6-multiwindow-multi-burn-rate-alerts):
a pair fires when the burn rate is above its factor over **both** a long window and a short window
(one twelfth of it), so it fires on a real burn and stops soon after the burn ends. Table 5-8
([recommended parameters](https://sre.google/workbook/alerting-on-slos/#recommended_parameters_for_an_slo_based_a))
gives, for a 30-day 99.9% SLO:

| Severity | Long | Short | Burn rate | Budget consumed |
|----------|------|-------|-----------|-----------------|
| page | 1h | 5m | 14.4 | 2% |
| page | 6h | 30m | 6 | 5% |
| ticket | 3d | 6h | 1 | 10% |

The factor is `budgetPercent / 100 × period / long`, with the SLO's period (read or given, 5.2):
2% × 720 h / 1 h = 14.4. The preset computes it from `budgetPercent`, as Sloth's window files are
written (Sloth's `getBurnRateFactor`, `internal/alert/window.go:116-125`), so `google-30d` gives
14.4, 6, 3 and 1 (Table 5-8 plus Sloth's ticket pair 2h/1d at 10%), and `google-28d` (same
percents, 28-day period) gives 13.44, 5.6, 2.8 and 0.93. The `pyrra` set uses Pyrra's factors as
given, 14, 7, 2 and 1, with Pyrra's windows for the period (for 28 days 1h/5m, 6h/30m, 1d/2h,
4d/6h; `slo/rules.go:1448-1475`); the first two pairs are critical, which the preset maps to page,
the last two warning, mapped to ticket. The reconciler sets:

- `burning: page` when any page pair has both windows above its factor;
- else `burning: ticket` when any ticket pair does;
- else `none`.

A NaN error ratio (no requests in the window) in a pair's **long** window is a burn rate of 0. In a
pair's **short** window it counts as the long window's burn rate: a quiet five minutes is not
evidence that a burn stopped, and treating it as 0 would let anyone who can pause the service's
traffic clear a page burn while the hour still burns. The comparisons are made in Go
from the values in the one response, so status records the rates that decided it. Alert `for:`
durations are not applied: a gate is re-evaluated, it does not page.

## 6. The gate: CEL variable versus Graph `readyWhen`

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `slo.<name>.*` in PolicyGate CEL, built from MetricCheck status by the PolicyGate reconciler, as `metrics.*` | Composes with exceptions, approvals, windows and actor policy in one expression; staleness is decided at evaluation with the reconciler's clock; the reason names the SLO and its numbers; `kardinal why`, `policy simulate` and overrides work | One more variable |
| (b) | A Graph `readyWhen` on the MetricCheck node, `${check.status.errorBudget.burning != "page"}` | Graph-native | `readyWhen` does not gate dependents in a standalone Graph (G1); a non-per-promotion MetricCheck is not a Graph node; no clock for staleness (G8); no reason, override or exception |
| (c) | Only `metrics.<name>.result` (the threshold on the budget) | No new variable | One bit: no burn state, no window, no "unknown" distinct from "spent" |

**Choice: (a).** `slo` is declared in `newEvaluator` as `DynType`
(`pkg/reconciler/policygate/cel_evaluator.go:107-121`), and `buildContext` (`reconciler.go:555`)
fills it from the same MetricCheck list as `metrics` (`reconciler.go:606-622`), with the same
namespace rule and the same per-promotion instance resolution (`buildMetricsContext`,
`reconciler.go:824-861`). The namespace rule is `metricsNamespace` (`reconciler.go:777-804`): an
org gate reads only the org policy namespace's checks, so a team cannot satisfy it with a check of
the same name in its own namespace; a team gate reads the Pipeline namespace's. One exception is
not copied: when an instance names an org policy namespace whose template is gone, `metrics` falls
back to the Pipeline namespace (`:797-800`), so during a GitOps prune-and-recreate, or for good if
the re-render fails (`docs/policy-gates.md:67-70`), a team check of the same name decides the org
gate. `slo` does not fall back: it is empty for such an instance, so every check it names is
missing (spent, in the pattern of §8). Only checks with `spec.errorBudget` appear in `slo`, and
`slo` is always set, to `{}` when the MetricCheck list fails, as `metrics` is
(`reconciler.go:617-622`); an unset variable would be a `no such attribute` error instead.

| Variable | Type | Value |
|----------|------|-------|
| `slo.<name>.known` | bool | the result is fresh (before `validUntil`); the query worked; the budget, the objective and the period are present and finite; every window's ratio is present (a NaN ratio means no requests, not a missing value, 5.3); and `status.errorBudget.lastKnownAt` equals `status.lastEvaluatedAt` (so values an older controller left in place are not read as fresh, §16) |
| `slo.<name>.stale` | bool | `validUntil` unset or passed (the controller or its writes stopped) |
| `slo.<name>.budgetRemaining` | double | share of the budget left, 1.0 to below 0; **absent unless `known`** |
| `slo.<name>.burnRate` | map(string, double) | per window, `slo.x.burnRate["1h"]`; absent unless `known` |
| `slo.<name>.burning` | string | `none`, `ticket` or `page`; absent unless `known` |
| `slo.<name>.noTraffic` | bool | every window's error ratio was NaN (no requests); present whether or not `known` |
| `slo.<name>.objective` | double | when read or given |
| `slo.<name>.window` | string | the SLO's period, `30d` |
| `slo.<name>.unknownMinutes` | int | whole minutes since `status.errorBudget.lastKnownAt`; 0 while known; 2147483647 when `lastKnownAt` is unset (never known, or cleared by a spec change), so deleting an instance or editing its spec never restarts a bounded fail-open |
| `slo.<name>.reason` | string | the check's `status.reason` |
| `slo.<name>.groups[<key>]` | map | the same fields per group (section 10); for a fleet target's gate, one entry per target of its fleet |

The absent fields make the plain expression fail closed: `slo.x.budgetRemaining > 0.0` is a
"no such key" evaluation error while the value is unknown, and an evaluation error blocks the gate
with the reason `CEL evaluation error: ...` (`reconciler.go:403-410`, `cel_evaluator.go:216-219`),
as for metrics today. `spec.message` explains a false expression only, never an error
(`reconciler.go:337-342`), so the reason gains an slo note naming each unknown check the gate reads
and why, as `staleMetricNotes` does for stale metrics (`reconciler.go:867-878`):
`CEL evaluation error: no such key: budgetRemaining; slo "checkout-availability" unknown for 3m (prometheus query error: ...)`.
A gate that wants something else says so in CEL (section 7).

**The numbers in every reason.** A passing gate's reason is `<expression> = true`
(`cel_evaluator.go:228`), and a gate whose approval policy decides reports the tally, so the budget
that let a version through would be recorded nowhere. The reason of every gate that reads `slo`, in
its expression or `requiredWhen`, passing or not, ends with one line per check it reads
(`slo "checkout-availability": budget 0.412, burning none, known`). The step's `status.decision`
(`api/v1alpha1/promotionstep_types.go:510-515`) records each required gate's reason as the step read
it when it left Pending, and the receipt records the decision (`PromotionDecision`,
`promotionreceipt_types.go:217-233`, at most 128 KiB, `MaxDecisionBytes`), so the numbers are in the
signed record.

**Wake-ups.** A gate re-evaluates when a MetricCheck it reads changes. The MetricCheck watch
(`metricCheckRequests`, `reconciler.go:1302-1320`) enqueues instances through
`instanceGateRequests` (`:1505-1511`), which reads only `spec.expression`. A gate that reads `slo`
only in `approval.requiredWhen`, the pattern of §8, would not wake on a budget change and would
wait for its `recheckInterval` (5m by default, `policygate_types.go:24`). So the watch uses
`instanceGateRequestsWhere` (`:1514`) with a `readsSLO` predicate over the expression and
`requiredWhen`, as `readsAgent` does for the AgentIndex watch (`actor_context.go:47-52`), still
waking gates that read `metrics`. The checks are substring matches, which over-enqueue ("slow"
contains "slo") at the cost of an evaluation and never miss one. The update predicate
(`metricCheckResultChanged`, `reconciler.go:1330-1342`) also passes changes of
`status.errorBudget.{budgetRemaining, burning, noTraffic}`, of `known` (`lastKnownAt` moving with
`lastEvaluatedAt`, or not), and of a group's values.

Examples:

```yaml
# Freeze prod while the budget is spent or a fast burn is on.
expression: 'slo["checkout-availability"].budgetRemaining > 0.0 && slo["checkout-availability"].burning != "page"'
message: "prod is frozen while checkout's error budget is spent"
```

When the expression is false the gate's `status.reason` names the numbers:
`slo "checkout-availability": budget remaining -0.12 (objective 0.999 over 30d), burning page (1h 16.2, 5m 21.0)`.

## 7. Staleness, and Prometheus down

### 7.1 States

| State | How it arises | Status | `slo.x` |
|-------|---------------|--------|---------|
| Known | Query answered with every series; budget, objective and period finite | values, `lastKnownAt` = `lastEvaluatedAt`, `result` from threshold | `known: true`, numbers present |
| No traffic | Every window's error ratio NaN (0/0), the budget a number | `noTraffic: true`; burn rates 0; the budget as recorded | `known: true`, `noTraffic: true` |
| Budget NaN | The budget series is NaN: Sloth's optimized period rule after an idle 5 minutes (3.1), or no requests over the whole period | `result: Fail`, reason `budget series is NaN (...)`; values cleared; `lastKnownAt` kept | `known: false`; `noTraffic` as computed |
| No data | A series is missing: rules not loaded, tool not running, wrong matcher, window not recorded, an error counter never exported (5.2) | `result: Fail`, reason names the missing series and these causes; values cleared; `lastKnownAt` kept | `known: false` |
| Query error | Prometheus unreachable, 5xx, timeout, egress refused, response over the read cap | `result: Fail`, reason `prometheus query error: ...` (`metriccheck/reconciler.go:227-231`); values cleared; `lastKnownAt` kept | `known: false` |
| Stale | No evaluation for three intervals (controller down, Limiter starved, status writes failing) | unchanged: going stale writes nothing, so no MetricCheck event wakes the gate | `stale: true`, `known: false`, from the gate's next evaluation, which is due at `validUntil` at the latest (below) |
| Suspended | `spec.suspend` (per-promotion instance of a finished Bundle) | unchanged until `validUntil` | stale after `validUntil` |
| Written by an older controller | After a downgrade, the older controller moved `lastEvaluatedAt` and left `status.errorBudget` as it was (§16) | unchanged until the next evaluation | `known: false` (`lastKnownAt` ≠ `lastEvaluatedAt`) |

**A NaN budget is unknown, never 1.0.** The first version of this spec mapped it to 1.0 "as Pyrra
does". Pyrra leaves an objective with no requests out of its API response and reports 1 only for a
NaN with requests (3.1), and Sloth's optimized period rule is NaN for a whole period after one idle
5 minutes, so the mapping would let a burning service through, and would let anyone who can stop a
service's traffic open its gate. A gate that wants to let quiet services through says so in its
expression with `slo.x.noTraffic` (6). `noTraffic` is false whenever the query failed or a series
was missing: it describes the values read, never a carried-over state.

### 7.2 Options

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Always fail closed | Safe; what MetricCheck does today | A Prometheus outage freezes every pipeline with a budget gate, including the fix for the outage |
| (b) | A spec switch on the check, `errorBudget.onUnknown: allow` | One field | Hides policy in the data source; every gate reading the check inherits it; not visible in the gate's expression |
| (c) | Fail closed by default; the gate states any fail-open in CEL with `known` and `unknownMinutes` | The policy is in the gate, next to the exceptions; time-bounded fail-open is expressible; `policy simulate` shows it | Authors must write it; a careless `!slo.x.known \|\| ...` fails open forever |

**Choice: (c).** Fail closed unless the gate says otherwise, and make bounded fail-open easy:

```yaml
# Allow promotions through a Prometheus outage of up to 30 minutes, then freeze.
expression: >
  slo["checkout-availability"].known
    ? slo["checkout-availability"].budgetRemaining > 0.0
    : slo["checkout-availability"].unknownMinutes < 30
```

`unknownMinutes` is computed from `status.errorBudget.lastKnownAt` and the PolicyGate reconciler's
clock at evaluation, the same place metric staleness is decided (`reconciler.go:832`, the clock at
`:607-611`); no clock enters Graph CEL. A gate blocked or passed by this rule says so in its reason
(`slo "checkout-availability" unknown for 12m (prometheus query error: ...): allowed by expression`).
The docs warn that `!slo.x.known || ...` is an unbounded fail-open.

**The gate re-evaluates when a value it read expires.** A check goes stale because its evaluations
stopped, and going stale writes nothing, so no MetricCheck event wakes the gate: today it would keep
its last `ready: true` until its `recheckInterval` (5m by default; the reconciler requeues at it,
`policygate/reconciler.go:297,410,445`), and `checkRequiredGates` trusts that status. So the
PolicyGate reconciler requeues at `min(recheckInterval, the earliest validUntil of the slo and
metrics checks the evaluation read)`, the instant its last result stops being fresh, and a stale
check never passes past its `validUntil` (metrics included: `metricResultStale`,
`reconciler.go:832-835`, already reads a stale result as `Stale`, and gains the same wake-up). The
bound of a fail-open is as precise: a MetricCheck status change wakes the gate (6), and a value's
expiry does.

## 8. Hotfix exceptions

The workbook's policy exempts "P0 issues or security fixes". kardinal has several ways to say who
gets through; they compose in CEL.

| | Mechanism | Who can trigger it | Record |
|-|-----------|--------------------|--------|
| (a) | A Bundle label: `"kardinal.io/hotfix" in bundle.labels` | Anyone who may create or label the Bundle: **self-asserted** | the Bundle; the gate reason |
| (b) | The actor: `actor.kind == "human" && actor.verified` (18-actor-policy §2) | Whoever created the version, verified at admission: every person with the promoter role, and an agent running on a person's credentials (18-actor-policy §2) | the stamp |
| (c) | An approval only while the budget is spent: `approval.requiredWhen` (18-actor-policy §3) | An approver from `allowedGroups`, not the author (`excludeAuthor`, one-party rule) | `status.approvals`, `ApprovalRecorded` AuditEvents |
| (d) | A gate override: `kardinal override <pipeline> prod --gate <g> --reason ... --expires-in 2h` (`docs/overrides.md`) | Anyone with `policygates/override` (the approver role, `user-roles.yaml:137`) or update on PolicyGates in the Pipeline namespace, where org gate instances live too (`docs/policy-gates.md:219`). `createdBy` is verified by `<release>-gate-overrides` (`identity-admission.yaml:122`); an override lasts at most `gateOverrideMaxMinutes` (24 h by default, `pkg/reconciler/policygate/overrides.go:41-50,87-93`); a registered agent's override is recorded but never honoured (`overrides.go:121-125`) | `spec.overrides[]`, `GateOverridden` AuditEvent |
| (e) | A rollback hold (`kardinal rollback --hold`): the PolicyGate reconciler passes the hold's Bundle in the held environment after `VerifyHeldRollback` (`pkg/lifecycle/hold.go:209`) checks it, **except at an approval gate**: `HoldExemptible` never exempts one (`pkg/reconciler/policygate/reconciler.go:1601-1615`, #1449: a quorum of people is never bypassed by a hold) | Whoever may write `pipelines/hold` (admission-checked) | the hold, the `EXEMPT` reason, `HoldCreated` |
| (f) | The rollback label: `"kardinal.io/rollback" in bundle.labels` (`pkg/lifecycle/lifecycle.go:34`) | Anyone who may create the Bundle: **self-asserted** on create. `<release>-bundle-creator` only keeps the label of the controller's own Bundles from changing (`identity-admission.yaml:487-532`) | the Bundle |

**Choice: (c) as the documented pattern, with (d) as break-glass.** Labels ((a), (f)) are never an
exception on their own, and neither is a label with (b): (b) says only that a verified person
created the version, which is true of every promoter, so it is not a check against the author. Only
(c) puts a second person in the path: the workbook's "P0 issues or security fixes" exception with
someone other than the author agreeing, and the approval is evidence. A fast burn is often the
moment a fix must ship, so the pattern does not block a burn outright; it asks for the approval. A
worked team gate:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: error-budget
  namespace: checkout
  labels: {kardinal.io/scope: team, kardinal.io/applies-to: prod}
spec:
  # The budget decides only whether an SRE must approve. Unknown, or no check, counts as spent.
  expression: "true"
  approval:
    required: 1
    allowedGroups: [sre]
    excludeAuthor: true
    requiredWhen: >
      !(("checkout-" + environment.name) in slo) ||
      !slo["checkout-" + environment.name].known ||
      slo["checkout-" + environment.name].budgetRemaining <= 0.0 ||
      slo["checkout-" + environment.name].burning == "page"
```

`requiredWhen` is written so it never errors. The first operand is true when the environment has no
check (deleted, renamed, not created yet, or the MetricCheck list failed and `slo` is empty), so a
missing check counts as spent. CEL's `||` is true when any operand is true even if another errors
(cel-go v0.31.0, `interpreter/interpretable.go:341-371`; the evaluator does not enable exhaustive
evaluation, `cel_evaluator.go:265-268`), and when `known` is true the fields are present. An
evaluation error in `requiredWhen` would fail the gate closed even with an approval
(`actor_context.go:115-129`, 18-actor-policy §3), which is why the example does not rely on one.
The gate has no `message`: its expression never fails, and the reason is the approval tally.

A version that is not a hotfix waits for one SRE who is not its author while the budget is spent,
**and so does a rollback through a hold** (e). Teams that want held rollbacks through without the
approval put the budget in a gate without `spec.approval` (the negation of the `requiredWhen`
above as its expression), which a verified hold exempts, and use overrides (d) for hotfixes. A
context variable telling `requiredWhen` that the Bundle is a verified held rollback would let one
gate do both; it is a follow-up (§19 row 10), not this spec.

## 9. Freezes, stop-the-line, pause and holds

The freeze of 18-actor-policy §9 (a ChangeWindow with `spec.freeze`, held by the PromotionStep
reconciler in `holdIfFrozen`, `pkg/reconciler/promotionstep/freeze.go:47`; on main since #1869) is
an incident switch. The error-budget gate is standing policy. Options for how a spent budget
freezes:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A PolicyGate (sections 6 to 8) | Per pipeline and environment; CEL exceptions; reasons, overrides, approvals, `simulate`; no new hold path. Since v0.13 a new, edited or deleted gate is copied again into every version still promoting (`docs/policy-gates.md:44-70`; `gatesChanged`, `pkg/reconciler/bundle/reconciler.go:787-796`), so a new budget gate holds their environments that have not started | A step already started is not stopped by any gate |
| (b) | The MetricCheck reconciler writes a freeze ChangeWindow when the budget is spent, and deletes it when it recovers | Holds steps already started, at their next git step | A metric creating and deleting kill switches in nobody's name (the controller is already exempt from the freeze `createdBy` rule, `chart/kardinal-promoter/templates/freeze-admission.yaml:44,92`); exceptions only as freeze exemptions, not CEL; flapping freezes at the budget boundary; namespace-wide, not per SLO |
| (c) | The PromotionStep reconciler holds while a referenced budget is spent (`holdIfBudgetSpent`), like pause and freeze | Covers steps in flight | Logic outside the Graph that the owner must approve (a new use of the G8 "Holding an existing PromotionStep" row); no CEL exceptions; decides from another CRD's status |

**Choice: (a).** Rules for the combination:

- **Independent.** A freeze holds a step whatever its gates say; a ready budget gate does not lift a
  freeze, and a freeze exemption does not pass a budget gate. A gate override does not lift a
  freeze. Both must allow.
- **In flight.** The gate instance's `status.ready` is live: a budget that is spent after the gate
  passed re-blocks it at the next evaluation, and the step's start re-checks its required gates
  (`checkRequiredGates`, `pkg/reconciler/promotionstep/reconciler.go:2380`, called at `:964`). A step
  already waiting for a merge or a health check carries on, as with pause and freeze.
- **Adopting the gate mid-incident.** A new org gate reaches the environments not yet started of
  every version already promoting (above). To hold steps already started, freeze
  (`kardinal freeze --everyone -n <ns> --reason <why>`) or pause; the docs page says so next to the
  gate example.
- **Holds.** A hold exempts a budget gate without `spec.approval` for the held rollback, after
  `VerifyHeldRollback`. It never exempts an approval gate (8 (e)), so under the pattern of §8 a
  rollback during a spent budget waits for the SRE approval, or is overridden.
- **Reasons.** `kardinal why` shows the freeze on the step message and the budget on the gate
  reason; both appear when both hold.

## 10. Per-environment and per-fleet-target mapping

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | One MetricCheck per environment, chosen in CEL by name: `slo["checkout-" + environment.name]` | No new field; one gate template for every environment; a missing check counts as spent (8) | A fleet of 500 targets needs 500 checks, 500 queries per interval, 500 Limiter slots |
| (b) | A per-promotion template whose `matchers` use `{{ environment.name }}` | Exists (with 4.3); per-target instance | One instance and query per target per Bundle in flight; with the default one slot per namespace (`DefaultNamespaceSlots`, `metriccheck/reconciler.go:70-74`) 500 targets cannot all refresh within `validUntil`; the instance appears only once the target's upstreams are Verified (`pkg/graph/compact.go:575-576`); instances are created only from the Pipeline namespace (`pkg/graph/metriccheck.go:171`), so an org gate never reads one |
| (c) | **One grouped check**: `groupBy.label: cluster` returns one budget per label value in one query; the gate of fleet target `prod-eu-west` reads the group whose key is the target's name (`eu-west`), or the value of one of the target's labels | One query per interval for the whole fleet; status per group shows the fleet's SLO map | New field and status list (at most 512 groups); target-to-group resolution in the PolicyGate reconciler |

**Choice: (a) for environments, (c) for fleets; (b) stays for team gates' per-version (canary)
SLIs.** A fleet target's gate instance has `kardinal.io/environment: prod-eu-west`
(`pkg/graph/gates.go:289-306`); gate instances carry no `kardinal.io/fleet` label, only
PromotionSteps do (`pkg/graph/compact.go:481-482`). The reconciler finds the fleet and the target
with `graph.FleetOf(pipeline, env)` (`pkg/graph/fleet.go:278`) and takes the member of that fleet
whose environment name (`FleetTargetName(fleet, target)`, `fleet.go:43`, which is
`<fleet>-<target>`) is the gate's environment, rather than stripping a prefix, which is ambiguous
when one fleet's name is a prefix of another's (`prod`, `prod-eu`). With `match: targetName` the
group key is the target's own name (`FleetTarget.Name`, `eu-west`); with `match: targetLabel` it is
the value of the target's label `targetLabel`, read from the Pipeline in the cache
(`spec.environments[].fleet.targets[].labels`, or `status.fleets[].targets[]` for selector members,
`api/v1alpha1/pipeline_types.go:249,1287`). `slo.<name>` is then that group's fields; a target with
no group is `known: false` with reason `no group cluster="eu-west"`.

For a fleet target's gate, `slo.<name>.groups` has **one entry per target of the fleet**, keyed by
its group key, with `known: false` for a target the query returned no group for: a target whose
series vanished is not skipped. A fleet-wide rule also checks the top-level `known`, which is false
when the query failed (and `groups` would then hold only unknown entries):
`slo.x.known && slo.x.groups.all(k, slo.x.groups[k].known && slo.x.groups[k].burning != "page")`.

**Who controls the mapping.** Target names and labels are Pipeline spec (`pipelines/edit`), or
labels on the Applications and ClusterProfiles a selector fleet selects
(`pipeline_types.go:796-798`). A team that may edit them can point a target at a healthy group. A
fleet-wide rule (`groups.all`) does not depend on the mapping; an org gate that maps per target
should key groups on labels the org owns (ClusterProfile labels the platform team writes). §15.1.

## 11. RBAC, admission, and kinds that are not installed

**The chosen design.** No new grants. The controller already queries Prometheus for MetricChecks.
Graph identities already create per-promotion MetricCheck instances
(`chart/kardinal-promoter/templates/graph-rbac.yaml:19-25`). User roles already read `metricchecks`
(`user-roles.yaml:68`); only `<release>-admin-extra` writes them (`user-roles.yaml:186-189`).
`<release>-graph-objects` (`identity-admission.yaml:542`) admits per-promotion instances (labelled
`kardinal.io/bundle`) only from kro and the controller; a MetricCheck written by hand is exempt
(`userMetricCheck`, `identity-admission.yaml:618-625,644`), so the per-environment checks of §10 are
protected by RBAC and by the agent rule below. The preset reads no Sloth or Pyrra object, so
whether their CRDs are installed does not matter: without the tool there are no series, and the
check reports `No data` with the series it looked for
(`no series slo:period_error_budget_remaining:ratio{sloth_service="checkout",sloth_slo="requests-availability"}: is Sloth generating rules for this SLO?`).

**Status is the controller's (#2446).** `<release>-admin-extra`'s `resources: ["*"]` covers the
status subresources too (in RBAC, `*` matches subresources; the chart's comment at
`user-roles.yaml:190` and `rulesGrant` in `test/helm/chart_test.go` say otherwise), and today no
admission rule protects `policygates/status` or a hand-written check's status. A namespace admin
can patch a check's `status.errorBudget`, or a gate instance's `status.ready`, which
`checkRequiredGates` trusts (`pkg/reconciler/promotionstep/reconciler.go:2380-2404`). That is inside
the trust boundary the security guide states (namespace `edit` and `admin` holders are owners as far
as kardinal can check, and can already pass as kro through impersonation,
`docs/guides/security.md:1059-1071`, ledger G16), but it is a one-command forgery, and the same
holds for every kardinal status outside the Graph's kinds (a Pipeline's, a Bundle's). #2446
(v0.13.1) adds a controller-only rule for them; this spec depends on it, since a budget gate's whole
input is a MetricCheck's status.

**Agents may not change a gate's inputs** (new; §15.1). `<release>-risk-owners`, the admission policy
of spec 25 §11 (a ValidatingAdmissionPolicy with the AgentIndex as params; whichever spec lands
first creates it), also refuses, for a requester the AgentIndex names as an agent, when the chart
value `admission.agentGateInputs` is `refuse` (the default; `allow` renders none of these rules):

- a CREATE, UPDATE or DELETE of a MetricCheck written by hand;
- a CREATE, UPDATE or DELETE of a PolicyGate template. Creating is refused too: most gates only add
  a condition, but a skip-permission gate in an org policy namespace lets versions skip an
  org-gated environment (`docs/policy-gates.md:640-650`).

How it matches, so it costs kro nothing and misses nothing:

- **Templates and hand-written checks only.** An `objectSelector` of `kardinal.io/bundle`
  `DoesNotExist` (an objectSelector matches when either the old or the new object matches, so a
  label added or removed in the write is still judged, as `userMetricCheck`'s has-or-had test does);
  kro and the controller are left out by a match condition before the params are read. The API
  server converts the whole AgentIndex for every request a policy with params matches
  (`actor-admission.yaml:330-335`), and kro re-applies every gate instance on every Graph pass, so
  instances must not reach the policy. Instances need no rule here: their spec is kro's and the
  controller's (`<release>-gate-overrides`, `identity-admission.yaml:217-229`; graph-objects for
  MetricChecks), an agent's override on one is recorded but never honoured (8 (d)), and a deleted
  instance is recreated by the Graph.
- **DELETE** reads `oldObject` (`object` is null on DELETE).
- **While the AgentIndex is not Ready, a person is never refused.** The index is not Ready before
  its first build (no status yet), with `CompactFormat` (a later release wrote it: an upgrade or a
  rollback in progress), and with `IndexFull`, the only reason the controller writes `Ready=False`
  (`pkg/reconciler/agentidentity/reconciler.go:193-198`); a controller outage leaves the condition
  as it was. In those states the rule cannot tell an agent from a person, so it refuses only
  ServiceAccount requesters (`system:serviceaccount:` usernames, kro and the controller already left
  out), whatever kind they write; once the index is Ready, it refuses whoever the index names as an
  agent, ServiceAccount or not. A GitOps tool's ServiceAccount applying gate templates in such a
  window is refused, with the reason, and retries; a person, including whoever fixes a bad org gate,
  is never held by the index. **What this cannot do:** an agent registered by a User or Group
  subject cannot be told from a person while the index is not Ready, so in that window it can
  change a gate template or a hand-written check (a ServiceAccount agent cannot: every
  ServiceAccount is refused). The actor policies, in the same window, follow "stopping is always
  allowed, going needs verification" (#2451, #2462): they refuse ServiceAccounts, never refuse a
  person a stop-the-line write (a freeze, a pause, an exemption, an override, a version), stamp a
  person's write `unverified`, never `human` (a raw Approval or Verdict from a requester that is
  not a ServiceAccount must carry that stamp), and count `unverified` as an agent for restrictions
  and as nobody for grants, so a User or Group agent passing as a person gains nothing there. This
  policy reads the AgentIndex, not stamps, so `unverified` changes nothing here, and the residual
  above stands. The rule applies to every rule of `<release>-risk-owners`, spec 25's included
  (spec 25 §11 is amended to match).
- **With no AgentIndex at all, everyone is refused.** `<release>-risk-owners` binds its AgentIndex
  param with `parameterNotFoundAction: Deny`, as `<release>-actor` does
  (`actor-admission.yaml:207`): the API server refuses every write the policy matches before any
  expression runs, people's included. The index is missing only if it is deleted, which
  `<release>-agent-index` refuses to everyone but the controller, so only with that policy's
  binding removed; the controller then recreates it. This is the one exception to "a person is
  never refused", and it fails closed.

A person and the controller are never judged, a bot (a ServiceAccount the index does not name) only
while the index is not Ready, and an agent the AgentIndex does not
name (one running on a person's credentials, or one whose change a GitOps bot applies; 15.1). The
gap predates this spec: no admission rule judges these writes today (`<release>-actor` matches
Bundles, Approvals, Verdicts and ChangeWindows, `actor-admission.yaml:197-206`), so an agent bound
to a namespace admin can repoint the check a team's gate reads, or edit the gate; since v0.13 an
edited or deleted template reaches every version still promoting at once
(`docs/policy-gates.md:44-70`). Budget gates make it matter: the gate's whole input is a check that
names a URL and a selector.

**What (a) and (b) would have cost**, recorded so nobody has to check again:

- **Grants.** Every Graph identity would need `get, list, watch` on
  `prometheusservicelevels.sloth.slok.dev` or `servicelevelobjectives.pyrra.dev` in a reader
  role, a reader RoleBinding in each namespace that holds SLOs (the G5 reader path #1283
  removed), and the same kinds in the `kro-watch` role aggregated to kro
  (`chart/kardinal-promoter/templates/graph-rbac.yaml`).
- **CRD not installed.** A literal GVK fails the compile of the whole Graph:
  `ResolveSchema` and `RESTMapping` errors are returned (`pkg/graphengine/compiler/context.go:265-278`).
  A dynamic GVK (`${}` in `apiVersion`, `context.go:242`) compiles, and at run time `mappingFor`
  returns `errSchemaNotReady` (`pkg/graphengine/executor/simple.go:1402-1406`), a soft not-ready:
  the node is Unresolved (`simple.go:424-427`), its dependents wait, and Graph `Ready` stays False
  until the CRD appears (G4). The translator's `servedKind` filter (`pkg/translator/translator.go:276`)
  could drop the node instead, but a gate that silently loses its SLO input must then fail closed,
  which needs the gate to know the node was dropped.
- **SLO object missing** (not created yet, deleted, renamed): `applyRef` returns `ErrNotReady` on
  NotFound (`simple.go:1211-1214`); the node is Unresolved and every node whose template reads it
  is data-pending. There is no optional `ref` that publishes "absent" and lets the gate decide:
  [G25](16-graph-capability-ledger.md#g25-a-ref-cannot-be-optional).
- **Wake-ups.** Sloth rewrites its status on each resync (15m by default,
  `cmd/sloth/commands/k8scontroller.go:96`, with a new `lastPromOpRulesSuccessfulGenerated`,
  `internal/storage/k8s/k8s.go:98-114`; `internal/app/kubecontroller/handler.go:133-140`), and skips
  status-only events for 3 minutes after a success (`handler.go:74-76,196-209`), so under G20 (a ref
  wakes on every write) a ref would wake about once per resync per SLO: cheap here, and it would
  not be for a status that carried a live budget.

**Admission and tenancy.** An org budget gate reads only MetricChecks in the org policy namespace
(`metricsNamespace`, 6), so a team cannot satisfy it with a check of the same name in its own
namespace. A team's preset can read any series its Prometheus URL serves, as a plain MetricCheck
can today. The egress guard on the Prometheus client (`metriccheck/prometheus.go:46-55`,
`pkg/egress/guard.go:12-25`) refuses loopback, link-local, metadata, unspecified and multicast
addresses and allows private ones, so a team can point its check at a Service of its own that
answers anything; that decides only the team's own gates, which the team can edit anyway. Operators
who want every check on the shared Prometheus set `egress.allowlist` to its host
(`chart/kardinal-promoter/values.yaml:622-630`). Matcher values are escaped by the reconciler, and
placeholders are rendered by the Graph builder with the safe-value rule (`RenderMetricText`,
`pkg/graph/metriccheck.go:91-105`, once matchers join `metricCheckSpec`, 4.3), so a matcher cannot
inject PromQL.

## 12. Failure modes

| Failure | Effect | Why it is safe or what to do |
|---------|--------|------------------------------|
| Prometheus down | `known: false`; gates fail closed unless they state a bounded fail-open | 7.2 |
| kardinal controller down | MetricCheck stale after three intervals; gates are not evaluated either | Nothing promotes without the controller |
| Sloth or Pyrra rules deleted | `No data`, fail closed, reason names the series | 11 |
| Wrong matchers, two SLOs match | `selector matches 2 SLOs`, fail closed | 5.1 |
| Budget flaps around 0 | Gate flips each evaluation | Values rounded; a gate can add hysteresis in CEL (`budgetRemaining > 0.02`) |
| No traffic (at night) | `noTraffic`, burn rates 0, the budget as recorded: allowed while the budget is a number and above the gate's bound | A NaN budget is unknown, never 1.0 (7.1); the docs recommend Sloth's `disableOptimized` |
| Sloth's optimized period rule NaN | `known: false` with `budget series is NaN`; fail closed for the period | 3.1: `disableOptimized` |
| An error counter never exported | `No data` for the burn-rate series | 5.2: initialize it to 0 |
| Tool upgrade renames series or windows | `No data`, fail closed | The pinned conventions file and METRIC-EB-07 per pinned tool version (18) |
| Limiter starvation (many SLOs, one slot per namespace) | Checks go stale, fail closed | Grouped checks (10); raise `metricCheck.querySlots.perNamespace`; org checks share the org namespace's slots (13) |
| A grouped response over the read cap | `response over 2 MiB`: query error, fail closed | The aggregated query (5.1), tested at 512 groups with 63-character keys |
| The environment has no check | `slo["checkout-" + environment.name]` is missing: an evaluation error that blocks, even with an approval, unless the expression tests `in slo` | The pattern of §8 tests it |
| Gate created mid-incident | Reaches the environments not yet started of versions in flight (v0.13); started steps go on | Freeze or pause for those (9) |
| Downgrade, then upgrade again | Status values left by the older controller | `known` needs `lastKnownAt` = `lastEvaluatedAt` (6, 16) |
| A namespace admin patches a check's or a gate's status | Until #2446, a forged `ready` or budget can start a step | #2446's controller-only status rule (11) |
| The AgentIndex is not Ready (before its first build, `CompactFormat`, `IndexFull`) | `<release>-risk-owners` refuses the writes it judges from ServiceAccounts, never from a person | The message says why and how to recover; a GitOps tool retries (11) |

## 13. Scale

- **Queries.** One instant query per check per interval (5.1), of recorded series only. The
  Limiter runs at most 12 queries at once in the cluster and 1 per namespace by default
  (`DefaultGlobalSlots`, `DefaultNamespaceSlots`, `metriccheck/reconciler.go:70-74`; the chart's
  `metricCheck.querySlots`). 1,000 SLOs spread over namespaces at the default 1 m interval fit while
  a query takes under about 0.7 s (12 slots × 60 s / 1,000); many checks in one namespace share its
  one slot, so a namespace with more than about 60 / (query seconds) checks needs a longer
  interval, grouped checks or more slots. **The per-namespace cap is one value for every
  namespace** (`pkg/slots/limiter.go`), and an org gate reads only the org namespace's checks, so
  every org budget check shares that namespace's slots: at 0.2 s a query and one slot, about 300
  checks per minute. An org with more SLOs raises the per-namespace slots, lengthens the interval of
  its checks, or groups them. A fleet of 500 targets is one grouped query.
- **Response and status size.** A check's status is about 1 KB; a grouped check with 512 groups and
  12 windows about 200 KB, under the object limit. The grouped response is aggregated to at most
  about 1.2 MiB, under the 2 MiB read cap of grouped preset checks (5.1).
- **Gate wake-ups.** A budget value changes on almost every evaluation. Rounding (budget to 0.001,
  burn rates to 0.01) bounds how often the predicate passes; burn-state and `known` changes always
  pass. Each wake re-evaluates only the instances whose expression or `requiredWhen` reads `slo` or
  `metrics` (`readsSLO`, 6) in that namespace (and an org namespace's instances).
- **Many pipelines, one SLO.** Within one namespace, many gates read one non-per-promotion check
  from the cache: one query, N evaluations. Across team namespaces each team has its own check; an
  org gate that should read each team's SLO is one gate per team (with `selector`), since CEL has no
  `pipeline` variable (`docs/reference/cel-context.md`) and `bundle.labels` are set by the
  version's creator.
- **Many agents.** Agent-authored versions read the same gates; nothing here is per actor.

## 14. Evidence

| Where | What |
|-------|------|
| Gate `status.reason` | The SLO, budget, burn state and the deciding windows (6), or why unknown, with the slo note (6, 7) |
| `kardinal why` (`-o wide`; `kardinal explain` is deprecated, #1758), UI stage drawer | The reason, plus a budget line from `slo.*`; `kardinal policy simulate` shows the `slo.*` values it used |
| PR evidence | The Policy Gate Compliance table (Gate, Namespace, Result, Reason, Last Evaluated; `pkg/scm/pr_template.go:311-322`) shows each gate's result and reason when the PR opens |
| Promotion receipts | The signed decision carries each gate's reason (`status.decision.gates[].reason`, `api/v1alpha1/promotionreceipt_types.go:217-233`), so the budget numbers that let a version through are in the signed record |
| AuditEvents | Unchanged: gate results, approvals (`ApprovalRecorded`), overrides (`GateOverridden`) |
| `kubectl get metricchecks` | New printer columns `Budget` (`.status.errorBudget.budgetRemaining`, the lowest group for a grouped check) and `Burning`, beside Provider, Result, LastValue and Age (`metriccheck_types.go:362-365`) |

## 15. What this does not protect against

- **An SLO that measures the wrong thing.** kardinal reads what Sloth, Pyrra or the rules record.
- **The source data.** Whoever writes the Sloth or Pyrra objects, the PrometheusRules or the scrape
  targets decides the numbers; kardinal reads them.
- **Self-asserted labels**, used alone or with only the actor check (8). The docs show them only
  with an approval.
- **Overrides of org gates.** Whoever has `policygates/override` (the approver role) or update on
  PolicyGates in the Pipeline namespace can override any gate instance there, an org budget gate
  included, for at most `gateOverrideMaxMinutes` (24 h by default), with a verified `createdBy` and a
  `GateOverridden` AuditEvent (`docs/policy-gates.md:219`). Org gates are enforced against team
  gates and team checks, not against a team's break-glass.
- **Namespace admins.** A person who is a namespace admin can repoint, suspend or delete the team's
  check or gate (the team's own policy), and, as kardinal's security guide states, can pass as kro in
  the namespace through impersonation (`docs/guides/security.md:1059-1071`, ledger G16). Org gates
  in the org namespace are the enforcement point against teams, not against their admins. Status
  forgery is closed by #2446; agents are refused (11).
- **Agents the AgentIndex does not name.** An agent running on a person's credentials, or one whose
  change a GitOps bot applies, is judged as that person or bot (11). The control is registration
  (spec 17) and the repository's review rules.
- **The fleet target mapping** a team controls (10).
- **Environment names.** Renaming `prod` in a Pipeline drops a gate selected by
  `kardinal.io/applies-to: prod`, org gates included (existing behaviour,
  `pkg/graph/builder.go:631-660`), and nothing refuses an agent's rename today; spec 25's
  `<release>-risk-owners` refuses only `spec.risk` changes. A follow-up (§19 row 10).
- **`{{ bundle.version }}` in a per-promotion matcher** is the first image's tag
  (`pkg/graph/metriccheck.go:107-121`), which the version's creator chooses: a new digest under an
  old, healthy tag reads the old version's series. Match on `{{ bundle.imageDigest }}` when the SLI's
  series carry it.
- **`budgetRemainingMetric`** names any series (`up` is always 1): it is the check owner's choice,
  covered by who may write the check (11).
- **Unbounded fail-open** written by a gate author (`!slo.x.known || ...`): it is visible in the
  expression and in `simulate`, not prevented.
- **Promotions already merged** are not rolled back by a spent budget; RollbackPolicy is for that.

### 15.1 Can an agent make a budget gate pass while the budget is spent?

Every input of the decision, who sets it, and what keeps an agent (or the person whose version it
is) from using it.

| Input | Who sets it | Can it pass a spent budget? | What stops it |
|-------|-------------|-----------------------------|---------------|
| The budget numbers (`status.errorBudget`) | the MetricCheck reconciler, from Prometheus | **Today, yes for a namespace admin**: `admin-extra`'s `*` covers `metricchecks/status`, and a hand-written check's status is not admission-checked | #2446's controller-only status rule (v0.13.1), which this spec depends on; the source data is outside kardinal (15) |
| A gate's status (`ready`, `lastEvaluatedAt`) | the PolicyGate reconciler | **Today, yes for a namespace admin**: `checkRequiredGates` trusts it, and no rule matches `policygates/status` | #2446 |
| An org check's spec (URL, matchers, objective, windows, `suspend`) | the org namespace's writers | Not by a team | `metricsNamespace` (6): an org gate reads only the org namespace's checks, and `slo` has no fallback when the org template is gone (6) |
| A team check's spec | `<release>-admin-extra` in the namespace (`user-roles.yaml:186-189`) | **Yes, for an agent with that role**: repoint `prometheusURL` at a Service that answers anything (the egress guard allows private addresses), move `matchers` to a healthy SLO, lower `objective`, name a `budgetRemainingMetric` that is always 1, or set `suspend` | `<release>-risk-owners` refuses an agent's create, change or delete of a hand-written MetricCheck (11). A person who is a namespace admin can (15) |
| A gate template (expression, `requiredWhen`, approval policy) | the namespace's PolicyGate writers | **Yes, for an agent with that RBAC**: an edited or deleted template reaches every version still promoting at once (v0.13), and a new skip-permission gate lets versions skip an org-gated environment | `<release>-risk-owners` refuses an agent's create, update or delete of a PolicyGate template (11) |
| A gate instance's spec | kro and the controller | No, except for a namespace admin who passes as kro (15) | `<release>-gate-overrides` (`identity-admission.yaml:217-229`): only kro and the controller create one or change anything but `spec.overrides` |
| A per-promotion check instance | kro and the controller | No; deleting one clears its status, and `unknownMinutes` restarts at its maximum, not 0 (6) | `<release>-graph-objects` |
| An override | `policygates/override` or update in the Pipeline namespace | Not for a registered agent: its override is recorded, never honoured (`overrides.go:121-125`) | A person can, for at most 24 h, recorded (15) |
| An approval (the pattern of §8) | approvers in `allowedGroups` | Not by the version's author (`excludeAuthor`); not by an agent unless the policy sets `allowAgents` | Spec 18's one-party rule |
| A hold | `pipelines/hold` | Only for a verified rollback, and never at an approval gate | `VerifyHeldRollback`, `HoldExemptible` (#1449) |
| Bundle labels (`hotfix`, `rollback`) | the version's creator | Only if a gate trusts them | The pattern never does (8) |
| The version's tag, in a `{{ bundle.version }}` matcher | the version's creator | Yes: an old, healthy tag on a new digest | Match on `{{ bundle.imageDigest }}` (15) |
| Traffic | whoever can stop the service's traffic | Not through the budget: while it is a number it is the real budget, and a NaN budget is unknown (7.1). Not through a page burn: a quiet short window takes the long window's rate (5.3) | 5.3, 7.1 |
| The fleet target mapping | `pipelines/edit`; the owners of selected Applications and ClusterProfiles | Yes, for a per-target org rule keyed on names the team controls | A residual (10): `groups.all`, or groups keyed on labels the org owns |
| An environment's name | `pipelines/edit` | Yes: renaming `prod` drops the gates that apply to `prod` | A residual for every gate, not only budget gates (15); follow-up |
| A missing check for an environment | whoever deletes or renames it | No: in the pattern, missing counts as spent; elsewhere it is an evaluation error | 8 |
| An agent the AgentIndex does not name | whoever runs it | As the person or bot it runs as | Registration and review rules (15) |

## 16. Upgrade from v0.13

- **`admission.agentGateInputs` is on (`refuse`) after the upgrade** (§11). A registered agent can no
  longer create, change or delete a PolicyGate template or a hand-written MetricCheck. While the
  AgentIndex is not Ready, no ServiceAccount can, GitOps controllers included: Argo CD's or Flux's
  ServiceAccount applying those objects is refused on `IndexFull`, and in the `CompactFormat` window
  of an upgrade or a rollback across the compact index (#2372), until the index is Ready again. The
  remedy is to trim AgentIdentities below the bound, or to set `admission.agentGateInputs: allow`.
  The release notes say so. People are never refused by the index's state; the actor policies'
  same rule for promotion actions is #2451.
- New optional fields on MetricCheck (`spec.errorBudget`, `status.errorBudget`) and a relaxed
  `query` rule. Existing checks and gates behave as before. The CRD change makes the
  implementation PR a "risky" one (no auto-merge), and so do the go.mod change
  (`github.com/prometheus/common`, in the module graph at v0.70.1 as an indirect dependency, becomes
  direct for `model.Duration`) and the e2e components of §18.
- `slo` is a new CEL variable. No existing expression can use it: an undeclared identifier fails
  compilation (`cel_evaluator.go:210-212`), so no gate written for v0.13 changes meaning.
- Downgrade. An older controller does not know `errorBudget`:
  - A hand-written preset check has an empty `query`, which Prometheus refuses (HTTP 400
    `bad_data`), recorded as `Fail` with the query error (`metriccheck/reconciler.go:227-231`). A
    gate that reads `slo` no longer compiles (undeclared variable) and blocks.
  - The older controller writes status with a merge patch of its own struct
    (`metriccheck/reconciler.go:331-333,395-404`), so `status.errorBudget` stays as the newer one
    left it while `lastEvaluatedAt` moves. After upgrading again, those values are not read as
    fresh, since `known` needs `lastKnownAt` equal to `lastEvaluatedAt` (6).
  - A per-promotion preset template: the older Graph builder copies the typed spec, which drops
    `errorBudget`, and CRDs are not rolled back (`docs/upgrade.md:21-30`), so the newer CRD rule
    refuses the instance and the Graph reports the apply error.
  All fail closed.
- Kubernetes 1.35 and newer (`chart/kardinal-promoter/Chart.yaml:13`). The new CRD rules use `all`
  and `matches`, which other kardinal CRDs (Pipelines, Bundles, Approvals) already use, and need no
  `double()`: `objective` is checked with a pattern. They are tested on the oldest and newest API
  servers the repository runs (`ENVTEST_K8S_MIN` 1.35.0 and `ENVTEST_K8S_MAX` 1.37.0) by a
  `TestErrorBudgetOnAPIServer` added to the `-run` list of the envtest target (`Makefile:103`),
  without which it never runs.

### 16.1 Defaults

| Setting | Default | Effect |
|---------|---------|--------|
| `MetricCheck.spec.errorBudget` | unset | No check reads a budget; existing checks are unchanged |
| `slo.*` | lists only checks with `spec.errorBudget` | A gate written for v0.13 cannot name it |
| The chart | ships no MetricCheck, no PolicyGate, no Sloth or Pyrra | Nothing is gated until a team or an org writes a gate |
| `alertWindows` | the tool's set for the SLO's period (5.2) | Factors follow the SLO's period |
| An unknown budget (no data, query error, stale, NaN) | blocks the gates that read it | Fail-open only as the gate states it (7.2) |
| `admission.agentGateInputs` (the §11 rules of `<release>-risk-owners`) | `refuse` | **On by default, a change from v0.13:** a requester the AgentIndex names as an agent may not create, change or delete a PolicyGate template or a hand-written MetricCheck; while the index is not Ready, a ServiceAccount may not; a person always may. `allow` renders none of these rules (11) |

## 17. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| EB1 | Where the budget comes from | Sloth `ref`; Pyrra `ref`; MetricCheck preset; Pyrra API via `web`; kardinal SLO CRD | **MetricCheck preset** (3.2): neither CRD's status has the budget; the preset reads the series both tools publish |
| EB2 | Gate mechanism | CEL `slo.*`; Graph `readyWhen`; `metrics.*` only | **CEL `slo.*`** (6): composes with exceptions; staleness needs the reconciler's clock; G1 |
| EB3 | Prometheus down | always closed; spec switch; closed by default with fail-open in CEL | **Closed by default, bounded fail-open in CEL** (7.2) via `known`, `unknownMinutes` |
| EB4 | Hotfix exceptions | label; actor; `requiredWhen` approval; override; rollback hold; rollback label | **`requiredWhen` approval as the pattern, overrides as break-glass** (8). Labels never count, alone or with the actor check (both are the author's); a hold exempts only gates without an approval policy (#1449), so held rollbacks wait for the approval under the pattern |
| EB5 | How a spent budget freezes | gate; automatic freeze ChangeWindow; step hold | **Gate** (9): policy, not a kill switch; freezes stay for incidents; since v0.13 a new gate reaches versions in flight |
| EB6 | Per-target mapping | per-environment checks; per-promotion template; grouped check | **Per-environment by name; grouped for fleets** (10): one query per fleet |
| EB7 | Burn-rate windows | fixed workbook table; tool-specific sets; computed from budget percent | **Named sets per tool, factors from `budgetPercent` or given** (5.3): the windows a tool records are the only ones that can be read |
| EB8 | Query shape | one query per value; one combined query; PromQL alert expression | **One combined query of raw values** (5.1): one Limiter slot; status shows the deciding rates |
| EB9 | Who may change a gate's inputs | RBAC only; refuse agents; only the org | **Refuse agents, on by default behind `admission.agentGateInputs`** (11, 15.1): an agent's write to a hand-written MetricCheck or a gate template is the way around any gate; people keep RBAC; org gates keep the org namespace (coordinator's decision on QA-2's review) |
| EB17 | The agent rules while the AgentIndex is not Ready | refuse every write they check, as the actor policies; refuse ServiceAccounts only; admit everything | **ServiceAccounts only** (11): the index's readiness never refuses a person, so policy can always be fixed, and a ServiceAccount waits for the index; a User or Group agent can change gate inputs until the index is Ready (11). The actor policies apply the same readiness rule (#2451, #2462) in the form "stopping is always allowed, going needs verification": `unverified` counts as an agent for restrictions and as nobody for grants. This policy reads no stamp and keeps admitting people: policy authoring can tighten or loosen, so it is neither stop nor go, and policy must stay fixable in an incident. A gate edit is recorded (a `GateChanged` AuditEvent reaches every version in flight), so a User or Group agent's edit in that window can be found afterwards. With no AgentIndex at all (`parameterNotFoundAction: Deny`), everyone is refused (coordinator's decision) |
| EB10 | A NaN budget | 1.0 (allow); unknown (fail closed) | **Unknown** (7.1): Sloth's optimized period rule is NaN for a whole period after one idle 5 minutes, and Pyrra reports 1 only with requests |
| EB11 | Pyrra's period | read `pyrra_window` first; required in spec | **Required** (5.2), checked against `pyrra_window`: the series names depend on it, and a first read would be a second request |
| EB12 | Sloth's window set | `google-30d` always; from the SLO's period | **From `slo:time_period:days`** (5.2), in the same query: Sloth chooses its set the same way, and both sets read the same windows |
| EB13 | Where burn rates are divided | PromQL; Go | **Go** (5.1): the series do not share label sets, and a PromQL division over two matching SLOs cannot name them |
| EB14 | A NaN ratio in a short window | burn rate 0; the long window's rate | **The long window's rate** (5.3): pausing traffic for five minutes must not clear a page burn |
| EB15 | What a passing gate records | `<expression> = true`; the slo values read | **The slo values**, on every reason of a gate that reads `slo` (6): the step's decision and the receipt then carry the numbers that let a version through |
| EB16 | `slo` when an org template is gone | fall back to the Pipeline namespace, as `metrics`; empty | **Empty** (6): a team check must never decide an org gate |

## 18. Test plan

Each layer names where the test lives today. Live suites are the first column of
`hack/e2e/matrix.txt`; coverage rows are `test/e2e/coverage.tsv`'s seven columns
(`id tier suite status area feature source`, `test/e2e/coverage/coverage.go:55`; `source` is a
`path#anchor`, `test/e2e/README.md:315-328`), claimed by a test's `Covers ID.` comment
(`README.md:307-313`). Every live test claims a row, or `e2e_coverage_test.go` fails.

**Unit** (`pkg/reconciler/metriccheck`, `pkg/reconciler/policygate`, `pkg/graph`, table-driven,
testify, `-race`):

- golden PromQL per source and option (matchers escaped, the regex for Pyrra, budget fallbacks, the
  grouped aggregation with `budget_count`), and window names (`1d` for 24h, `4w` for 28d; a
  `prometheus` source's suffix as written);
- Sloth's set from the period (28 gives google-28d, 30 gives google-30d, 7 is an error without
  `alertWindows`); Pyrra's windows for 28d and 30d from `pyrra.window` (for 30d `32m`, `1h4m`, ...),
  and `pyrra_window` and `pyrra_objective` mismatches;
- factors for `google-30d` (14.4, 6, 3, 1), `google-28d` (13.44, 5.6, 2.8, 0.93) and `pyrra`
  (14, 7, 2, 1 with scaled windows), and custom windows; `burning` for each pair crossing, and
  short-only and long-only crossings (must not fire);
- vector parsing with `kardinal_series` and `groupBy`: two matching SLOs, two in one group, missing
  series, NaN ratios (no traffic: burn rate 0; a NaN short window taking its long window's rate, so
  a page burn survives a quiet five minutes), a NaN budget (unknown), `+Inf`; `noTraffic` false on a
  query error and on No data;
- the largest grouped response (512 groups, 12 windows, 63-character group keys, 17-digit values)
  decoded under the 2 MiB cap; 513 groups failing the evaluation;
- rounding; the threshold on the unrounded value; the predicate passing on burn-state and `known`
  changes but not on sub-rounding changes;
- `known` false when `lastKnownAt` differs from `lastEvaluatedAt`; a `specHash` change clearing
  status, a `prometheusURL` edit keeping `lastKnownAt`; `unknownMinutes` at its maximum when
  `lastKnownAt` is unset; a read objective matching a given one to within 1e-9 (99.9 and 99.99);
  a short window not shorter than its long one refused;
- CEL: absent fields fail closed, with the slo note in the reason; the bounded fail-open expression
  passes at 29 minutes and blocks at 30; `in slo` for a missing check, and on `slo` set to `{}` when
  the list fails; `slo` empty for an instance whose org template is gone; `slo.x.groups` for a fleet
  target found by `graph.FleetOf` (one entry per target, `known: false` for a target with no group,
  fleet names that prefix each other); the slo line on a passing gate's reason, in the step's
  `status.decision`;
- wake-ups: a MetricCheck change wakes a gate that reads `slo` only in `requiredWhen` (`readsSLO`);
  a gate requeues at the earliest `validUntil` of the checks it read, and a check that goes stale
  with no write blocks the gate at that instant, not at `recheckInterval`;
- `pkg/graph`: `ExprReadsMetric` on `slo.<name>` and in `requiredWhen`; matcher placeholders
  rendered, and leftovers refused by the reconciler; the upstream hold on `errorBudget` with no
  `query`;
- CRD rules (`api/v1alpha1/crd_cel_rules_test.go`, and `TestErrorBudgetOnAPIServer` in `test/helm`
  with `-tags envtest` on 1.35.0 and 1.37.0): exclusive `query` and `errorBudget`, source blocks,
  `groupBy` refused with pyrra, `threshold.text` refused with `errorBudget`, matcher keys (as the
  map-key rule of `kardinal.io_approvals.yaml:195`), metric names, durations (`30d`, `4w` and `1d1h`
  admitted; `1.5h`, `µs` and `5m1h` refused), `budgetPercent` and `factor`, `objective`;
- admission (`test/helm`, as `identity_admission_test.go`'s rows): `<release>-risk-owners` refuses an
  agent's create, update and delete of a hand-written MetricCheck and of a PolicyGate template;
  admits a person's and a bot's writes, and kro's and the controller's instances; judges a write
  that adds or removes `kardinal.io/bundle`; reads `oldObject` on DELETE; renders no rule with
  `admission.agentGateInputs: allow`. Its behaviour while the AgentIndex is not Ready (no status,
  `CompactFormat`, `IndexFull`: a person's write admitted, a ServiceAccount's refused) needs a real
  API server: a `TestRiskOwnersAvailabilityOnAPIServer` in the `Makefile:103` list, as
  `TestActorIndexAvailabilityOnAPIServer` is.

Safety assertions that must be shown to fail (the QA rule): fail closed on unknown, on a NaN budget
and on two matching SLOs; org namespace isolation, including no `slo` fallback; the agent refusals;
a gate woken by a change it reads only in `requiredWhen`; a gate re-evaluated when a value it read
expires; a quiet short window not clearing a page burn. Each PR breaks the property on purpose and shows the test failing.

**Fake-client journeys** (real builder and reconcilers on a fake client), named EBJ, in a new
`test/e2e/error_budget_journey_test.go` as `test/e2e/actor_policy_journey_test.go`, with scenario
variants in `test/scenario/policy`:

| # | Case | Expect |
|---|------|--------|
| EBJ1 | Budget spent, then recovered | prod blocked, then promotes |
| EBJ2 | Fast burn with budget left | prod blocked while `burning: page` |
| EBJ3 | The §8 pattern, budget spent: the author approves, then an SRE | the author's approval does not count; the SRE's does |
| EBJ4 | A held rollback while the budget is spent | passes a budget gate without `spec.approval`; waits for the approval under the §8 pattern |
| EBJ5 | An override on the budget gate | passes, then expires; an agent's override is not honoured |
| EBJ6 | A freeze and a ready budget gate | the freeze holds |
| EBJ7 | A fleet where one target's group burns | that target is held, the others promote |
| EBJ8 | Prometheus error, with and without the bounded fail-open | blocked; passes until the bound, then blocked |
| EBJ9 | The gate template edited while a version is in flight | the edited rule applies to its environments not yet started |
| EBJ10 | Traffic stops for 10 minutes during a page burn | `burning` stays `page`; the gate stays blocked |
| EBJ11 | An org gate's template deleted while a team check of the same name exists | the org gate does not pass on the team check |

**Live e2e** (the `flux` suite, which runs the Prometheus Operator, Prometheus and a Pushgateway;
its run pattern is `^Test(Flux|Metric|Obs)_`, so the tests are `TestMetric_ErrorBudget*`, beside
`test/e2e/live/metrics_test.go`; run nightly at `COUNT=1` and weekly at 3 by `e2e-live.yml`, and
each implementation PR shows a dispatch at `COUNT=2`). Rows to add to `test/e2e/coverage.tsv` with
the implementation; `source` is the user page of §19 row 8:

| id | tier | suite | status | area | feature |
|----|------|-------|--------|------|---------|
| METRIC-EB-01 | live | flux | todo | metriccheck | A sloth-source preset reads budget and burn rates from synthetic Sloth-named PrometheusRules (the suite's Prometheus loads every PrometheusRule) and writes `status.errorBudget` |
| METRIC-EB-02 | live | flux | todo | metriccheck | A gate on `slo.<name>.budgetRemaining` blocks prod while the budget is below 0 and opens when it recovers |
| METRIC-EB-03 | live | flux | todo | metriccheck | A page-severity burn (both windows above 14.4) blocks with budget left; a short-window-only spike does not |
| METRIC-EB-04 | live | flux | todo | metriccheck | Prometheus unreachable for one check (its `prometheusURL` moved to a Service with no endpoints, so the shared Prometheus keeps serving the parallel tests): the gate fails closed; with a 2-minute bounded fail-open and `recheckInterval: 10s` it passes, then blocks after the bound |
| METRIC-EB-05 | live | flux | todo | metriccheck | The §8 pattern: a spent budget wakes the gate (not its recheck) and requires an approval; an SRE's releases a hotfix, the author's does not |
| METRIC-EB-06 | live | flux | todo | metriccheck | A grouped check maps fleet targets to groups; one burning group holds only its target |
| METRIC-EB-07 | live | flux | todo | metriccheck | Real Sloth v0.16.0 and Pyrra v0.10.2 controllers (new `hack/e2e/components/sloth.sh` and `pyrra.sh`, pinned in `hack/e2e/versions.env`; Pyrra's operator with `--generic-rules` and its image pinned, since its v0.10.2 manifest still names v0.9.0) generate the series the presets read, one SLO each; Sloth's optimized period rule after an idle window reads as `budget series is NaN` |
| METRIC-EB-08 | live | core | todo | actors | An agent's write to a hand-written MetricCheck and to a PolicyGate template refused by `<release>-risk-owners`; a person's admitted (`TestGate_BudgetOwners*`, beside spec 25's RISK-07) |
| METRIC-EB-09 | live | scale | todo | metriccheck | 1,000 preset checks over 50 namespaces and one 512-group check at the default Limiter caps (`TestScale_ErrorBudget`) |
| METRIC-EB-10 | live | upgrade-v0130 | todo | metriccheck | v0.13 checks and gates unchanged by the upgrade; a downgrade and a second upgrade do not read old budget values as fresh (`TestUpgradeV0130_ErrorBudget`) |

**Scale** (the `scale` suite, which runs Prometheus; `^TestScale_`; METRIC-EB-09): `TestScale_ErrorBudget`, with
1,000 preset checks over 50 namespaces and one 512-group check at the default Limiter caps: every
check refreshes within `validUntil`, the grouped response decodes, and the gate wake-ups stay within
the rounding bound.

**Upgrade** (`upgrade-v0130`, once v0.13.0 is tagged; a `kardinal-v0130.sh` component, as
`kardinal-v0123.sh`; METRIC-EB-10): v0.13 checks and gates behave the same after the upgrade; a preset check
added after it reads its SLO; a downgrade and a second upgrade do not read the old values as fresh.

**Soak.** The enterprise-day soak (#2403, `TestSoak_EnterpriseDay` in its new heavy `soak` suite,
which runs Prometheus, weekly and in the release gate; invariants checked every 3 minutes by
`framework/soak` and once at the end by `framework/invariants`, #2404 and #2410; a mid-run
`helm upgrade`) is where the gate runs under load next to the other v0.14 features. Once this spec
is implemented, one tenant's prod gets the §8 pattern on a synthetic Sloth SLO whose budget the
soak spends and restores mid-run, and the soak adds invariants:

- no Bundle passed a budget gate while its SLO was spent or unknown without an approval by someone
  other than its author, an override, or a fail-open its gate states;
- every budget gate re-evaluated within one interval of a budget change (no wake missed);
- no agent write to a hand-written MetricCheck or a gate template was admitted;
- no budget check went stale (past `validUntil`) while the controller ran.

## 19. Work breakdown (issues to open under a v0.14.0 epic)

None of these exists yet (audited 2026-10-21: no code, no issue).

| # | Issue | Size | Depends on |
|---|-------|------|------------|
| 1 | `feat(api)`: MetricCheck `spec.errorBudget` and `status.errorBudget` (types incl. `specHash`, CRD rules with `all` and `matches`, ordered Prometheus durations, `threshold.text` exclusive, printer columns), `TestErrorBudgetOnAPIServer` in the envtest list. Risky PR (CRD) | m | none |
| 2 | `feat(metriccheck)`: the preset query builder and vector reader for sloth, pyrra and prometheus (raw values, the grouped aggregation with unaggregated metadata and the 2 MiB grouped cap, window names with `model.Duration`, Sloth's set from the period, Pyrra's windows from `pyrra.window`, the objective tolerance), burn rates and `burning` in Go (a quiet short window takes the long window's rate), rounding, the states of 7.1 (a NaN budget unknown, `lastKnownAt`, `specHash`), the pinned conventions file. Risky PR (go.mod) | l | 1 |
| 3 | `feat(graph)`: per-promotion presets: `ExprReadsMetric` reads `slo` and `requiredWhen`; matcher placeholders rendered and refused; the upstream hold on `errorBudget` | s | 1 |
| 4 | `feat(policygate)`: the `slo.*` variable (`known`, `unknownMinutes` with its maximum, groups, `{}` on a list failure, no org-template fallback), the slo lines in every reason of a gate that reads `slo`, the `readsSLO` wake-up over the expression and `requiredWhen`, the update predicate, the requeue at the earliest `validUntil` it read (7.2); `docs/reference/cel-context.md` and its docs test | m | 2; #2446 (status is the controller's) |
| 5 | `feat(policygate)`: fleet target to group resolution through `graph.FleetOf` (target name and target label), one `groups` entry per fleet target | s | 4 |
| 6 | `feat(chart)`: `<release>-risk-owners` refuses agents' writes to hand-written MetricChecks and to PolicyGate templates, behind `admission.agentGateInputs` (default `refuse`; objectSelector on `kardinal.io/bundle`, kro and the controller left out before the params are read, `oldObject` on DELETE, only ServiceAccounts refused while the index is not Ready, for every rule of the policy), with helm tests and `TestRiskOwnersAvailabilityOnAPIServer`. Shared with spec 25 §18 row 13 (#2425): whichever lands first creates the policy | s | none |
| 7 | `feat(cli,ui)`: the budget in `kardinal why`, `policy simulate` and the stage drawer | s | 4 |
| 8 | `docs`: the error-budget gate page (Sloth with `disableOptimized`, Pyrra with `--generic-rules`, plain-rule recipes, the Pyrra `web` recipe and its caveats, fail-open guidance, the hotfix pattern and holds, the freeze interplay, the gaming table of 15.1, `egress.allowlist`); `docs/metric-checks.md` and `docs/policy-gates.md`; the regenerated API reference; changelog | m | 4, 6 |
| 9 | `test(e2e)`: METRIC-EB-01..10 with their coverage rows, `sloth.sh` and `pyrra.sh` with pins, `TestScale_ErrorBudget`, the `upgrade-v0130` test, and the soak invariants in #2403. Risky PR (e2e components) | m | each feature; 8 (the rows' `source`); #2403 |
| 10 | Follow-ups, not v0.14: a verified held-rollback variable so `requiredWhen` can exempt held rollbacks (8); `metrics`' org-template fallback (6); an agent's rename of an environment that org gates apply to (15); a recovery forecast (1.1) | s | 4 |
