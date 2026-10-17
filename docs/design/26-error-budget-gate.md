# Design: Error-budget gate

> Status: Proposed (spec for v0.14.0, AGENTIC-PLAN §5 "Error-budget gate"). Implementation waits
> until v0.13.0 is tagged and follows in separate PRs.
> Created: 2026-10-15
> Depends on: [18-actor-policy.md](18-actor-policy.md) (`actor.*`, `approval.requiredWhen`, freezes
> and stop-the-line, §9), and the existing MetricCheck and PolicyGate reconcilers.
> Verified against: this repository at `main` (`8f5d86e`); kro v0.10.0-rc.0 (`54a203b`) at
> `/local/home/rrroizma/kro-upstream`; Sloth [v0.16.0](https://github.com/slok/sloth/tree/v0.16.0)
> and Pyrra [v0.10.2](https://github.com/pyrra-dev/pyrra/tree/v0.10.2), the latest releases on
> 2026-10-15, read from their tagged sources; the Google
> [SRE workbook](https://sre.google/workbook/table-of-contents/), chapter 5 "Alerting on SLOs" and
> appendix B "Example Error Budget Policy". Source references are `path:line` in those trees.
> Nothing here is implemented yet.

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
| Reading the budget and burn rates | The MetricCheck reconciler queries Prometheus and writes its own status (`status.errorBudget`) | (2) a reconciler that writes its own CRD status; the I/O is the existing MetricCheck side-effect class (G8, "MetricCheck query slots" row) |
| The freeze | A PolicyGate whose CEL reads `slo.<name>.*`, built from MetricCheck status by the PolicyGate reconciler, as `metrics.*` | (3) a CEL variable; the read joins the existing G8 "Cross-CRD reads for the gate context" row (MetricChecks are already in it) |
| Per-promotion SLO checks (canary) | A `perPromotion` MetricCheck template with `errorBudget`, instantiated by the Graph as an owned node, as today | (2) owned node |
| Hotfix exceptions | CEL in the gate expression and `approval.requiredWhen`; gate overrides | (3) CEL; existing override admission |

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

Every series carries `sloth_service`, `sloth_slo` and `sloth_id` (`conventions/slo.go:21-23`,
`GetSLOIDPromLabels`). `sloth_id` is `<service>-<slo name>` (`internal/storage/io/k8s_sloth.go:139`):
it does not include the namespace, so two namespaces with the same service and SLO names record
colliding series unless their `spec.labels` differ.

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
computed per request by querying Prometheus (`main.go:855`, `main.go:942-956`:
`remaining = (budget.total - errors/total) / budget.total`, and `remaining = 1` when it is NaN, i.e.
no requests). In Prometheus, Pyrra records:

| Series | Value | When |
|--------|-------|------|
| `<metric>:burnrate<window>{slo="<name>"}` | the **error ratio** over `<window>` (not divided by the budget); its alerts compare it with `factor * (1 - target)` (`slo/rules.go:139-148`) | always; `<metric>` is the indicator's total metric without `_total`/`_count` (`slo/rules.go:431-449`) |
| `pyrra_objective`, `pyrra_window`, `pyrra_availability{slo="<name>"}` | target ratio, window in seconds, availability over the window | only with `--generic-rules`, which defaults to `false` (`main.go:84`; `slo/rules.go:1498-1590`) |

The `slo` label is the SLO's name (`slo/rules.go:596-608`).

### 3.2 Options

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A `ref` node on the Sloth `PrometheusServiceLevel`; a gate or `readyWhen` reads its status | Graph-native; no query | The status has no budget (3.1): the gate could learn only that rules were generated. A missing SLO object holds every node that reads it (G25); a CRD not installed fails the whole Graph compile with a literal GVK (G4); RBAC on a third-party kind for every Graph identity (section 11) |
| (b) | A `ref` node on the Pyrra `ServiceLevelObjective` | Same | Same: the status is `{type}` only. Budget and availability come from Pyrra's API server, which a `ref` cannot call |
| (c) | **A burn-rate preset on MetricCheck** (`spec.errorBudget`): the MetricCheck reconciler reads the recorded series from Prometheus and writes budget, burn rates and alert state to its own status | Uses the series both tools actually publish, and plain recording rules for teams without either tool; the existing provider, egress guard, Limiter, staleness and per-promotion instances apply unchanged; no third-party RBAC | One more field on MetricCheck; the preset must know each tool's series names (versioned conventions, section 5.2) |
| (d) | The `web` provider against Pyrra's API (`POST http://<pyrra-api>:9099/objectives.v1alpha1.ObjectiveService/GetStatus`, JSONPath `{.status[0].budget.remaining}`; port from `main.go:468`) | No code | Pyrra only; one number per check (no burn rates); a second hop that queries Prometheus on each call; the Connect JSON request shape is not verified against a running Pyrra |
| (e) | A new kardinal `ErrorBudget` CRD and reconciler | Clean status of its own | Duplicates MetricCheck (provider, egress, Limiter, staleness, instances, admission); a second way to gate on a metric |

**Choice: (c).** (a) and (b) fail on the facts: the CRs do not carry the budget. If a future Sloth
or Pyrra release writes budget into status, a `ref` could replace the query for that tool; the
cost (G4, G25, section 11) stays and is listed so nobody has to check it again. (d) remains
documented as a no-code recipe for Pyrra users who want only `budget.remaining`.

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
    matchers: {cluster: prod-eu}           # optional extra equality matchers
    alertWindows: google-30d               # the default for sloth; or google-28d, pyrra, or windows: [...]
```

Pyrra (generic rules on for the budget; without them, burn rates only, with `objective` given):

```yaml
  errorBudget:
    source: pyrra
    pyrra:
      slo: checkout-availability           # the ServiceLevelObjective name (the `slo` label)
      objective: "0.999"                   # a ratio (Pyrra's spec.target "99.9" is a percentage); optional with --generic-rules
      window: 28d                          # optional with --generic-rules (read from pyrra_window)
    alertWindows: pyrra                    # the default for pyrra: Pyrra's own windows and factors
```

Plain recording rules, named as in the workbook (`job:slo_errors_per_request:ratio_rate1h`):

```yaml
  errorBudget:
    source: prometheus
    prometheus:
      errorRatioMetric: "job:slo_errors_per_request:ratio_rate"   # the window is appended: ...rate5m
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
type ErrorBudgetSpec struct {
	// +kubebuilder:validation:Enum=sloth;pyrra;prometheus
	Source     string                 `json:"source"`
	Sloth      *SlothErrorBudget      `json:"sloth,omitempty"`
	Pyrra      *PyrraErrorBudget      `json:"pyrra,omitempty"`
	Prometheus *PrometheusErrorBudget `json:"prometheus,omitempty"`

	// Matchers are extra equality matchers added to every series read.
	// Keys are Prometheus label names; values are escaped by the reconciler
	// and may hold MetricCheck placeholders (instances only).
	// +kubebuilder:validation:MaxProperties=8
	Matchers map[string]string `json:"matchers,omitempty"`

	// AlertWindows names a window set: google-30d, google-28d or pyrra.
	// Default: pyrra for source pyrra, google-30d otherwise.
	// +kubebuilder:validation:Enum=google-30d;google-28d;pyrra
	AlertWindows string `json:"alertWindows,omitempty"`
	// Windows replaces the set. At most 6.
	Windows []BurnWindow `json:"windows,omitempty"`

	// GroupBy reads one SLO per value of a label, for fleets (section 10).
	GroupBy *ErrorBudgetGroupBy `json:"groupBy,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.budgetPercent) != has(self.factor)",message="set budgetPercent or factor"
type BurnWindow struct {
	// +kubebuilder:validation:Enum=page;ticket
	Severity string `json:"severity"`
	// BudgetPercent is the share of the period's budget this pair detects
	// (the workbook's "error budget consumed"), e.g. "2". The factor is
	// computed from it (section 5.3).
	BudgetPercent string `json:"budgetPercent,omitempty"`
	// Factor is the burn-rate threshold itself, e.g. "14".
	Factor string `json:"factor,omitempty"`
	Short         string `json:"short"` // "5m"
	Long          string `json:"long"`  // "1h"
}

type SlothErrorBudget struct {
	Service string `json:"service"` // sloth_service
	SLO     string `json:"slo"`     // sloth_slo
}

type PyrraErrorBudget struct {
	SLO       string `json:"slo"`                 // the slo label
	Objective string `json:"objective,omitempty"` // ratio, e.g. "0.999"
	Window    string `json:"window,omitempty"`
}

type PrometheusErrorBudget struct {
	ErrorRatioMetric      string `json:"errorRatioMetric"`
	Objective             string `json:"objective"`
	Window                string `json:"window"`
	BudgetRemainingMetric string `json:"budgetRemainingMetric,omitempty"`
}

type ErrorBudgetGroupBy struct {
	Label string `json:"label"` // a Prometheus label, e.g. cluster
	// +kubebuilder:validation:Enum=targetName;targetLabel
	Match       string `json:"match,omitempty"`       // default targetName
	TargetLabel string `json:"targetLabel,omitempty"` // the FleetTarget label key when match is targetLabel
}

// In MetricCheckStatus:
type ErrorBudgetStatus struct {
	Objective       string           `json:"objective,omitempty"`       // "0.999"
	Window          string           `json:"window,omitempty"`          // "30d"
	BudgetRemaining string           `json:"budgetRemaining,omitempty"` // "0.412"; negative when overspent
	BurnRates       []BurnRateSample `json:"burnRates,omitempty"`       // +listType=map +listMapKey=window
	Burning         string           `json:"burning,omitempty"`         // none | ticket | page
	NoTraffic       bool             `json:"noTraffic,omitempty"`
	LastKnownAt     *metav1.Time     `json:"lastKnownAt,omitempty"`     // last evaluation with every value
	Groups          []ErrorBudgetGroup `json:"groups,omitempty"`        // +kubebuilder:validation:MaxItems=512
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

Numbers are strings, as `status.lastValue` is (Kubernetes API conventions avoid floats). Values are
rounded: budget to 3 decimals, burn rates to 2 (section 13, gate wake-ups).

### 4.3 What stays as it is

- **`status.lastValue` and `status.result`.** `lastValue` is the budget remaining; `threshold`
  applies to it, so `metrics.<name>.result == "Pass"` keeps working and means "budget left" with
  `{operator: gt, value: 0}`. `threshold` stays required: the schema does not change for existing
  checks. With `groupBy`, `lastValue` is the lowest budget of any group.
- **`status.validUntil`** (three intervals, at least 30 s; `pkg/reconciler/metriccheck/reconciler.go:57-60`)
  covers the new fields.
- **`perPromotion` and `suspend`** apply (`api/v1alpha1/metriccheck_types.go:78-99`). A per-promotion
  preset with `matchers: {version: "{{ bundle.version }}"}` measures the new version's own SLI, for
  canaries.
- **Validation.** `errorBudget` requires `provider: prometheus`. The rule "query is required for every
  provider except web" (`metriccheck_types.go:18`) becomes "... except web, and except prometheus
  with errorBudget"; `query` and `errorBudget` are exclusive. Label names match
  `^[a-zA-Z_][a-zA-Z0-9_]*$`; metric names `^[a-zA-Z_:][a-zA-Z0-9_:]*$`; `objective` is in (0, 1);
  durations use the MetricCheck interval pattern.

## 5. Queries

### 5.1 One request per evaluation

The reconciler builds one PromQL instant query that returns every value it needs, tagged with a
`kardinal_series` label through `label_replace`, joined with `or`:

```
label_replace(<budget>, "kardinal_series", "budget", "", "")
or label_replace(<burn 5m>, "kardinal_series", "burn5m", "", "")
or label_replace(<burn 1h>, "kardinal_series", "burn1h", "", "")
or ...
```

`or` keeps every element because the `kardinal_series` values differ. One request means one Limiter
slot per SLO per interval, as a plain MetricCheck costs today. The provider gains a vector reader
next to `extractScalar` (`pkg/reconciler/metriccheck/prometheus.go:185-229`): without `groupBy`,
exactly one element per `kardinal_series` value, else the evaluation fails with
`selector matches N SLOs` (the existing one-series rule).

### 5.2 Per source

With `M` the matchers (the source's own labels plus `spec.errorBudget.matchers`, escaped):

| Value | sloth | pyrra | prometheus |
|-------|-------|-------|------------|
| error ratio at `w` | `slo:sli_error:ratio_rate<w>{M}` | `{__name__=~".+:burnrate<w>", M}` | `<errorRatioMetric><w>{M}` |
| objective | `slo:objective:ratio{M}` | `pyrra_objective{M}` or `objective` | `objective` |
| budget remaining | `slo:period_error_budget_remaining:ratio{M}` | `(pyrra_availability{M} - pyrra_objective{M}) / (1 - pyrra_objective{M})`; with `objective` given and no generic rules: unavailable | `budgetRemainingMetric{M}`, else `1 - <errorRatioMetric><window>{M} / (1 - objective)` |
| burn rate at `w` | error ratio / `slo:error_budget:ratio{M}` (Sloth's own template, `metadata_rules_v1/plugin.go:131-134`) | error ratio / `(1 - objective)` | error ratio / `(1 - objective)` |

Constraints, stated so nobody relies on more:

- **Only recorded windows.** Sloth records error ratios only for its alert windows and the period
  (`sli_rules_v1/plugin.go:58-60`), Pyrra only for its own windows (`slo/rules.go:1445-1476`: 5m,
  30m, 1h, 2h, 6h, 1d, 4d for 28 days). A preset window the tool does not record returns no series:
  the check reports which series is missing (section 7) and the gate fails closed. The default
  set of each source is the tool's own: `google-30d` and `google-28d` are Sloth's
  `internal/alert/windows/google-30d.yaml` and `google-28d.yaml` (pages 5m/1h and 30m/6h, tickets
  2h/1d and 6h/3d), and `pyrra` is Pyrra's `Windows(period)`. A Sloth window set on a Pyrra SLO
  asks for `burnrate3d`, which Pyrra does not record.
- **Pyrra's `__name__` regex** finds the burn-rate series without naming the indicator metric. A
  second recording rule with the same suffix and `slo` label would make two elements:
  `selector matches 2 SLOs`, fail closed.
- **Sloth's `sloth_id`** ignores the namespace (3.1). In a shared Prometheus, set `matchers` to a
  label that tells the SLOs apart (`namespace`, or one from Sloth's `spec.labels`).
- **Series names are tool conventions,** not APIs. They are pinned per source in one Go file with
  the tool version they were read from, and a contract test checks them (section 18).

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

The factor is `budgetPercent / 100 × period / long` (2% × 720 h / 1 h = 14.4). The preset computes
it from `budgetPercent`, as Sloth's window files are written, so `google-30d` gives 14.4, 6, 3 and 1
(Table 5-8 plus Sloth's ticket pair 2h/1d at 10%), and `google-28d` (same percents, 28-day period)
gives 13.44, 5.6, 2.8 and 0.93. The `pyrra` set uses Pyrra's factors as given, 14, 7, 2 and 1, with
windows scaled to the period (1h/5m, 6h/30m, 1d/2h, 4d/6h for 28 days; the first two are
critical, which the preset maps to page, the last two warning, mapped to ticket;
`slo/rules.go:1451-1475`). The reconciler sets:

- `burning: page` when any page pair has both windows above its factor;
- else `burning: ticket` when any ticket pair does;
- else `none`.

The comparisons are made in Go from the burn rates in the one response, not in PromQL, so status
records the rates that decided it. Alert `for:` durations are not applied: a gate is re-evaluated,
it does not page.

## 6. The gate: CEL variable versus Graph `readyWhen`

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `slo.<name>.*` in PolicyGate CEL, built from MetricCheck status by the PolicyGate reconciler, as `metrics.*` | Composes with exceptions, approvals, windows and actor policy in one expression; staleness is decided at evaluation with the reconciler's clock; the reason names the SLO and its numbers; `kardinal explain`, `why`, `policy simulate` and overrides work | One more variable |
| (b) | A Graph `readyWhen` on the MetricCheck node, `${check.status.errorBudget.burning != "page"}` | Graph-native | `readyWhen` does not gate dependents in a standalone Graph (G1); a non-per-promotion MetricCheck is not a Graph node; no clock for staleness (G8); no reason, override or exception |
| (c) | Only `metrics.<name>.result` (the threshold on the budget) | No new variable | One bit: no burn state, no window, no "unknown" distinct from "spent" |

**Choice: (a).** `slo` is declared in `newEvaluator` as `DynType`
(`pkg/reconciler/policygate/cel_evaluator.go:106-118`), and `buildContext` fills it from the same
MetricCheck list as `metrics` (`pkg/reconciler/policygate/reconciler.go:547-563`), with the same
namespace rule (`metricsNamespace`: an org gate reads only the org namespace's checks) and the same
per-promotion instance resolution (`buildMetricsContext`, `reconciler.go:729-770`). Only checks with
`spec.errorBudget` appear in `slo`.

| Variable | Type | Value |
|----------|------|-------|
| `slo.<name>.known` | bool | the result is fresh (before `validUntil`), the query worked and every value is present |
| `slo.<name>.stale` | bool | `validUntil` unset or passed (the controller or its writes stopped) |
| `slo.<name>.budgetRemaining` | double | share of the budget left, 1.0 to below 0; **absent unless `known`** |
| `slo.<name>.burnRate` | map(string, double) | per window, `slo.x.burnRate["1h"]`; absent unless `known` |
| `slo.<name>.burning` | string | `none`, `ticket` or `page`; absent unless `known` |
| `slo.<name>.noTraffic` | bool | the error ratios were NaN (no requests) |
| `slo.<name>.objective` | double | when read or given |
| `slo.<name>.window` | string | `30d` |
| `slo.<name>.unknownMinutes` | int | whole minutes since `status.errorBudget.lastKnownAt` (0 while known; large when never known) |
| `slo.<name>.reason` | string | the check's `status.reason` |
| `slo.<name>.groups[<key>]` | map | the same fields per group (section 10) |

The absent fields make the plain expression fail closed: `slo.x.budgetRemaining > 0.0` is a
"no such key" evaluation error while the value is unknown, and an evaluation error blocks the gate
with a reason, as for metrics today. A gate that wants something else says so in CEL (section 7).

The gate wake-up path is the MetricCheck watch: `metricCheckRequests` (`reconciler.go:1184`)
enqueues instances whose expression reads `metrics` or `slo`, and the update predicate
(`metricCheckResultChanged`, `reconciler.go:1212-1224`) also passes changes of
`status.errorBudget.{budgetRemaining, burning, noTraffic}` and of a group's values.

Examples:

```yaml
# Freeze prod while the budget is spent or a fast burn is on.
expression: 'slo["checkout-availability"].budgetRemaining > 0.0 && slo["checkout-availability"].burning != "page"'
message: "prod is frozen while checkout's error budget is spent"
```

The gate's `status.reason` when it blocks names the numbers:
`slo "checkout-availability": budget remaining -0.12 (objective 0.999 over 30d), burning page (1h 16.2, 5m 21.0)`.

## 7. Staleness, and Prometheus down

### 7.1 States

| State | How it arises | Status | `slo.x` |
|-------|---------------|--------|---------|
| Known | Query answered with every series | values, `lastKnownAt` = now, `result` from threshold | `known: true`, numbers present |
| No traffic | Error ratios NaN (0/0) | `noTraffic: true`; budget 1.0 if the budget series is NaN too (as Pyrra does, `main.go:955-956`); burn rates 0 | `known: true`, `noTraffic: true` |
| No data | A series is missing (rules not loaded, tool not running, wrong matcher, window not recorded) | `result: Fail`, reason names the missing series; values cleared; `lastKnownAt` kept | `known: false` |
| Query error | Prometheus unreachable, 5xx, timeout, egress refused | `result: Fail`, reason `prometheus query error: ...` (`metriccheck/reconciler.go:203-208`); values cleared; `lastKnownAt` kept | `known: false` |
| Stale | No evaluation for three intervals (controller down, Limiter starved, status writes failing) | unchanged | `stale: true`, `known: false` |
| Suspended | `spec.suspend` (per-promotion instance of a finished Bundle) | unchanged until `validUntil` | stale after `validUntil` |

### 7.2 Options

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Always fail closed | Safe; what MetricCheck does today | A Prometheus outage freezes every pipeline with a budget gate, including the fix for the outage |
| (b) | A spec switch on the check, `errorBudget.onUnknown: allow` | One field | Hides policy in the data source; every gate reading the check inherits it; not visible in the gate's expression |
| (c) | Fail closed by default; the gate states any fail-open in CEL with `known` and `unknownMinutes` | The policy is in the gate, next to the exceptions; time-bounded fail-open is expressible; `policy simulate` shows it | Authors must write it; a careless `!slo.x.known || ...` fails open forever |

**Choice: (c).** Fail closed unless the gate says otherwise, and make bounded fail-open easy:

```yaml
# Allow promotions through a Prometheus outage of up to 30 minutes, then freeze.
expression: >
  slo["checkout-availability"].known
    ? slo["checkout-availability"].budgetRemaining > 0.0
    : slo["checkout-availability"].unknownMinutes < 30
```

`unknownMinutes` is computed from `status.errorBudget.lastKnownAt` and the PolicyGate reconciler's
clock at evaluation, the same place metric staleness is decided (`reconciler.go:737`); no clock
enters Graph CEL. A gate blocked or passed by this rule says so in its reason
(`slo "checkout-availability" unknown for 12m (prometheus query error: ...): allowed by expression`).
The docs warn that `!slo.x.known || ...` is an unbounded fail-open. The bound is as precise as the gate's
re-evaluation: a MetricCheck status change wakes it, and otherwise its `recheckInterval` (default
5m) does.

## 8. Hotfix exceptions

The workbook's policy exempts "P0 issues or security fixes". kardinal has several ways to say who
gets through; they compose in CEL.

| | Mechanism | Who can trigger it | Record |
|-|-----------|--------------------|--------|
| (a) | A Bundle label: `"kardinal.io/hotfix" in bundle.labels` | Anyone who may create or label the Bundle: **self-asserted** | the Bundle; the gate reason |
| (b) | The actor: `actor.kind == "human" && actor.verified` (18-actor-policy §2) | Whoever created the version, verified at admission | the stamp |
| (c) | An approval only while the budget is spent: `approval.requiredWhen` (18-actor-policy §3) | An approver from `allowedGroups`, not the author (`excludeAuthor`, one-party rule) | `status.approvals`, `ApprovalRecorded` AuditEvents |
| (d) | A gate override: `kardinal override <pipeline> --stage prod --gate <g> --reason ... --expires-in 2h` (docs/overrides.md) | Anyone who may update PolicyGates in the namespace; verified `createdBy`, capped duration | `spec.overrides[]`, `GateOverridden` AuditEvent |
| (e) | A rollback hold (`kardinal rollback --hold`): the PolicyGate reconciler already passes the hold's Bundle in the held environment after `VerifyHeldRollback` checks it against the CRDs (ledger, Notes, "Environment holds") | Whoever may write `pipelines/hold` (admission-checked) | the hold, the `EXEMPT` reason, `HoldCreated` |
| (f) | The rollback label: `"kardinal.io/rollback" in bundle.labels` (`pkg/lifecycle/lifecycle.go:34`) | Anyone who may create the Bundle: **self-asserted**, no admission rule checks it | the Bundle |

**Choice: (c) as the documented pattern, with (e) for rollbacks and (d) as break-glass; labels
((a), (f)) only combined with (b) or (c).** A label alone lets any Bundle author skip the freeze,
which defeats it. An approval by someone else is the workbook's "P0 issues or security fixes"
exception with a second person, and the approval is evidence. A fast burn is often the moment a
fix must ship, so the pattern does not block a burn outright; it asks for the approval. A worked
team gate:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: error-budget
  namespace: checkout
  labels: {kardinal.io/scope: team, kardinal.io/applies-to: prod}
spec:
  # The budget decides only whether an SRE must approve. Unknown counts as spent.
  expression: "true"
  approval:
    required: 1
    allowedGroups: [sre]
    excludeAuthor: true
    requiredWhen: >
      !slo["checkout-" + environment.name].known ||
      slo["checkout-" + environment.name].budgetRemaining <= 0.0 ||
      slo["checkout-" + environment.name].burning == "page"
  message: "prod promotions need an SRE approval while checkout's error budget is spent or burning fast"
```

`requiredWhen` is written so it never errors: a CEL `||` is true when any operand is true, even if
another one errors, and when `known` is true the fields are present. An evaluation error in `requiredWhen` would fail the gate
closed even with an approval (18-actor-policy §3), which is why the example does not rely on one.
A rollback through a hold passes without the approval (e); a version that is neither waits for
one SRE who is not its author.

## 9. Freezes, stop-the-line, pause and holds

The freeze of 18-actor-policy §9 (a ChangeWindow with `spec.freeze`, held by the PromotionStep
reconciler in `holdIfFrozen`; designed on main, implemented in #1869, not on main) is an incident
switch. The error-budget gate is standing policy. Options for how a spent budget freezes:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | A PolicyGate (sections 6 to 8) | Per pipeline and environment; CEL exceptions; reasons, overrides, approvals, `simulate`; no new hold path | A new or edited gate applies to versions shipped after it (docs/policy-gates.md:44-57); versions in flight keep their gates |
| (b) | The MetricCheck reconciler writes a freeze ChangeWindow when the budget is spent, and deletes it when it recovers | Holds every Graph, in flight too | A metric creating and deleting kill switches; the freeze admission (`createdBy` equal to the caller) would need a controller exemption; exceptions only as freeze exemptions, not CEL; flapping freezes at the budget boundary; namespace-wide, not per SLO |
| (c) | The PromotionStep reconciler holds while a referenced budget is spent (`holdIfBudgetSpent`), like pause and freeze | Covers steps in flight | Logic outside the Graph that the owner must approve (a new use of the G8 "Holding an existing PromotionStep" row); no CEL exceptions; decides from another CRD's status |

**Choice: (a).** Rules for the combination:

- **Independent.** A freeze holds a step whatever its gates say; a ready budget gate does not lift a
  freeze, and a freeze exemption does not pass a budget gate. A gate override does not lift a
  freeze. Both must allow.
- **In flight.** The gate instance's `status.ready` is live: a budget that is spent after the gate
  passed re-blocks it at the next evaluation, and the step's start re-checks its required gates
  (`checkRequiredGates`, `pkg/reconciler/promotionstep/reconciler.go:2167`). A step already waiting
  for a merge or a health check carries on, as with pause and freeze.
- **Adopting the gate mid-incident.** A new org gate applies to the next versions only. To hold
  versions already promoting, freeze (`kardinal freeze --everyone -n <ns>`) or pause; the docs page
  says so next to the gate example.
- **Holds.** A rollback hold's Bundle passes the held environment's gates already (8 (e)), so a
  rollback during a spent budget needs nothing new.
- **Reasons.** `kardinal why` shows the freeze on the step message and the budget on the gate
  reason; both appear when both hold.

## 10. Per-environment and per-fleet-target mapping

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | One MetricCheck per environment, chosen in CEL by name: `slo["checkout-" + environment.name]` | No new field; one gate template for every environment; a missing check is an error (fail closed) | A fleet of 500 targets needs 500 checks, 500 queries per interval, 500 Limiter slots |
| (b) | A per-promotion template whose `matchers` use `{{ environment.name }}` | Exists; per-target instance | One instance and query per target per Bundle in flight; with the default one slot per namespace (`DefaultNamespaceSlots`, `metriccheck/reconciler.go:66-69`) 500 targets cannot all refresh within `validUntil`; the instance appears only once the target's upstreams are Verified |
| (c) | **One grouped check**: `groupBy.label: cluster` returns one budget per label value in one query; the gate of fleet target `prod-eu-west` reads the group whose key is the target name (`eu-west`), or the value of one of the target's labels | One query per interval for the whole fleet; status per group shows the fleet's SLO map | New field and status list (at most 512 groups); target-to-group resolution in the PolicyGate reconciler |

**Choice: (a) for environments, (c) for fleets; (b) stays for per-version (canary) SLIs.** For a
fleet target's gate (labels `kardinal.io/fleet: prod`, `kardinal.io/environment: prod-eu-west`;
`pkg/graph/fleet.go:21-22,42`), the reconciler strips `<fleet>-` to get the target name. With
`match: targetLabel` it reads the target's labels from the Pipeline (`spec.environments[].fleet.targets`,
or `status.fleets` for selector members) from the cache. `slo.<name>` is then that group's
fields; a target with no group is `known: false` with reason `no group cluster="eu-west"`.
`slo.<name>.groups` is always there, so a fleet-wide rule can read all of them
(`slo.x.groups.all(k, slo.x.groups[k].burning != "page")`).

## 11. RBAC, admission, and kinds that are not installed

**The chosen design.** No new grants. The controller already queries Prometheus for MetricChecks;
Graph identities already create per-promotion MetricCheck instances
(`chart/kardinal-promoter/templates/graph-rbac.yaml:20-24`); user roles already include
`metricchecks`; the identity admission policy already limits who creates instances. The preset
reads no Sloth or Pyrra object, so whether their CRDs are installed does not matter: without the
tool there are no series, and the check reports `No data` with the series it looked for
(`no series slo:period_error_budget_remaining:ratio{sloth_service="checkout",sloth_slo="requests-availability"}: is Sloth generating rules for this SLO?`).

**What (a) and (b) would have cost**, recorded so nobody has to check again:

- **Grants.** Every Graph identity would need `get, list, watch` on
  `prometheusservicelevels.sloth.slok.dev` or `servicelevelobjectives.pyrra.dev` in a reader
  role, a reader RoleBinding in each namespace that holds SLOs (the G5 reader path #1283
  removed), and the same kinds in the `kro-watch` role aggregated to kro
  (`chart/kardinal-promoter/templates/graph-rbac.yaml`).
- **CRD not installed.** A literal GVK fails the compile of the whole Graph:
  `ResolveSchema` and `RESTMapping` errors are returned (`pkg/graphengine/compiler/context.go:266-279`).
  A dynamic GVK (`${}` in `apiVersion`, `context.go:243`) compiles, and at run time `mappingFor`
  returns `errSchemaNotReady` (`pkg/graphengine/executor/simple.go:1402-1406`), a soft not-ready:
  the node is Unresolved (`simple.go:424-427`), its dependents wait, and Graph `Ready` stays False
  until the CRD appears (G4). The translator's `servedKind` filter (`pkg/translator/translator.go:285`)
  could drop the node instead, but a gate that silently loses its SLO input must then fail closed,
  which needs the gate to know the node was dropped.
- **SLO object missing** (not created yet, deleted, renamed): `applyRef` returns `ErrNotReady` on
  NotFound (`simple.go:1211-1214`); the node is Unresolved and every node whose template reads it
  is data-pending. There is no optional `ref` that publishes "absent" and lets the gate decide:
  [G25](16-graph-capability-ledger.md#g25-a-ref-cannot-be-optional).
- **Wake-ups.** Sloth writes its status each time it processes the object
  (`internal/app/kubecontroller/handler.go:133-139`) and skips updates that changed only the status
  (`handler.go:196-205`), so G20 (a ref wakes on every write) would be cheap here; it would not be
  for a status that carried a live budget.

**Admission and tenancy.** An org budget gate reads only MetricChecks in the org policy namespace
(`metricsNamespace`), so a team cannot satisfy it with a check of the same name in its own
namespace. A team's preset can read any series its Prometheus URL serves, as a plain MetricCheck
can today; Prometheus-side tenancy (a per-tenant proxy URL) is the operator's choice. Matcher
values are escaped by the reconciler and placeholders are rendered only with the safe-value rule
(`pkg/graph/metriccheck.go` `RenderMetricText`), so a matcher cannot inject PromQL.

## 12. Failure modes

| Failure | Effect | Why it is safe or what to do |
|---------|--------|------------------------------|
| Prometheus down | `known: false`; gates fail closed unless they state a bounded fail-open | 7.2 |
| kardinal controller down | MetricCheck stale after three intervals; gates are not evaluated either | Nothing promotes without the controller |
| Sloth or Pyrra rules deleted | `No data`, fail closed, reason names the series | 11 |
| Wrong matchers, two SLOs match | `selector matches 2 SLOs`, fail closed | 5.1 |
| Budget flaps around 0 | Gate flips each evaluation | Values rounded; a gate can add hysteresis in CEL (`budgetRemaining > 0.02`) |
| NaN at night (no traffic) | `noTraffic`, budget 1.0, burn 0: promotions allowed | Matches Pyrra's choice; a gate that wants otherwise reads `noTraffic` |
| Tool upgrade renames series | `No data`, fail closed | Contract test per pinned tool version (18) |
| Limiter starvation (many SLOs, one slot per namespace) | Checks go stale, fail closed | Grouped checks (10); raise `metricCheck.querySlots.perNamespace` |
| Gate created mid-incident | Applies to the next versions only | Freeze or pause for versions in flight (9) |

## 13. Scale

- **Queries.** One instant query per check per interval (5.1), of recorded series only. The
  Limiter runs at most 12 queries at once in the cluster and 1 per namespace by default
  (`metriccheck/reconciler.go:66-69`). 1,000 SLOs spread over namespaces at the default 1 m
  interval fit while a query takes under about 0.7 s (12 slots × 60 s / 1,000); many checks in one
  namespace share its one slot, so a namespace with more than about 60 / (query seconds) checks
  needs a longer interval, grouped checks or more slots. A fleet of 500 targets is one grouped
  query.
- **Status size.** A check is about 1 KB; a grouped check with 512 groups and 7 burn windows about
  150 KB, well under the object limit.
- **Gate wake-ups.** A budget value changes on almost every evaluation. Rounding (budget to 0.001,
  burn rates to 0.01) bounds how often the predicate passes; burn state changes always pass. Each
  wake re-evaluates only the instances whose expression reads `slo` or `metrics` in that namespace
  (and an org namespace's instances), as today.
- **Many pipelines, one SLO.** Many gates read one non-per-promotion check: one query, N
  evaluations from the cache.
- **Many agents.** Agent-authored versions read the same gates; nothing here is per actor.

## 14. Evidence

| Where | What |
|-------|------|
| Gate `status.reason` | The SLO, budget, burn state and the deciding windows (6), or why unknown (7) |
| `kardinal explain`, `why`, UI stage drawer | The reason, plus a budget line from `slo.*`; `kardinal policy simulate` shows the `slo.*` values it used |
| PR evidence | The Policy Gate Compliance row already shows each gate's result and reason when the PR opens |
| AuditEvents | Unchanged: gate results, approvals (`ApprovalRecorded`), overrides (`GateOverridden`) |
| `kubectl get metricchecks` | A printer column `Budget` (`.status.errorBudget.budgetRemaining`) and `Burning` |

## 15. What this does not protect against

- **An SLO that measures the wrong thing.** kardinal reads what Sloth, Pyrra or the rules record.
- **Self-asserted hotfix labels** used alone (8). The docs show the label only with an approval or
  the actor check.
- **Whoever may update PolicyGates in the namespace** can override a team gate; an org gate lives
  in the org namespace (existing override rules).
- **Unbounded fail-open** written by a gate author (`!slo.x.known || ...`): it is visible in the
  expression and in `simulate`, not prevented.
- **Promotions already merged** are not rolled back by a spent budget; RollbackPolicy is for that.

## 16. Upgrade from v0.13

- New optional fields on MetricCheck (`spec.errorBudget`, `status.errorBudget`) and a relaxed
  `query` rule. Existing checks and gates behave as before. The CRD change makes the
  implementation PR a "risky" one (no auto-merge).
- `slo` is a new CEL variable. No existing expression can use it: an undeclared identifier fails
  compilation, so no gate written for v0.13 changes meaning.
- Downgrade: an older controller does not know `errorBudget`, sends an empty query and records
  `Fail` with the query error; a gate that reads `slo` no longer compiles (undeclared variable) and
  blocks. Both fail closed.
- Kubernetes 1.35 and newer: the new CRD rules use only constructs the existing MetricCheck rules
  use.

## 17. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| EB1 | Where the budget comes from | Sloth `ref`; Pyrra `ref`; MetricCheck preset; Pyrra API via `web`; kardinal SLO CRD | **MetricCheck preset** (3.2): neither CRD's status has the budget; the preset reads the series both tools publish |
| EB2 | Gate mechanism | CEL `slo.*`; Graph `readyWhen`; `metrics.*` only | **CEL `slo.*`** (6): composes with exceptions; staleness needs the reconciler's clock; G1 |
| EB3 | Prometheus down | always closed; spec switch; closed by default with fail-open in CEL | **Closed by default, bounded fail-open in CEL** (7.2) via `known`, `unknownMinutes` |
| EB4 | Hotfix exceptions | label; actor; `requiredWhen` approval; override; rollback hold; rollback label | **`requiredWhen` approval as the pattern; rollback holds pass; labels only with a second check** (8): labels are self-asserted |
| EB5 | How a spent budget freezes | gate; automatic freeze ChangeWindow; step hold | **Gate** (9): policy, not a kill switch; freezes stay for incidents |
| EB6 | Per-target mapping | per-environment checks; per-promotion template; grouped check | **Per-environment by name; grouped for fleets** (10): one query per fleet |
| EB7 | Burn-rate windows | fixed workbook table; tool-specific sets; computed from budget percent | **Named sets per tool, factors from `budgetPercent` or given** (5.3): the windows a tool records are the only ones that can be read |
| EB8 | Query shape | one query per value; one combined query; PromQL alert expression | **One combined query, decision in Go** (5.1, 5.3): one Limiter slot, status shows the deciding rates |

## 18. Test plan

- **Unit** (`pkg/reconciler/metriccheck`, `pkg/reconciler/policygate`, table-driven, `-race`):
  - golden PromQL per source and option (matchers escaped, regex for Pyrra, budget fallbacks);
  - factors for `google-30d` (14.4, 6, 3, 1), `google-28d` (13.44, 5.6, 2.8, 0.93) and `pyrra`
    (14, 7, 2, 1 with scaled windows), and custom windows; `burning` for each pair crossing, and
    short-only and long-only crossings (must not fire);
  - vector parsing with `kardinal_series` and `groupBy`, duplicates, missing series, NaN, `+Inf`;
  - rounding, and the predicate passing on burn-state changes but not on sub-rounding changes;
  - CEL: absent fields fail closed; the bounded fail-open expression passes at 29 minutes and blocks
    at 30; `slo.x.groups` for a fleet target, a target with no group;
  - CRD validation (envtest on 1.35 and the newest): exclusive `query` and `errorBudget`, source
    blocks, label and metric name patterns.
  - Each safety assertion (fail closed on unknown, fail closed on two matching SLOs, org namespace
    isolation) is broken on purpose once and shown failing in the PR.
- **Fake-client journeys**: budget spent blocks prod and recovers; fast burn blocks with budget
  left; `requiredWhen` approval lets a hotfix through and the author's own approval does not count;
  a rollback through a hold passes; an override passes and expires; a freeze and a ready budget gate (freeze wins);
  a fleet where one target's group burns and the others promote; Prometheus error with and without
  the fail-open rule.
- **Live e2e** (flux suite, which runs Prometheus; COUNT=2). Rows to add to `test/e2e/coverage.tsv`
  with the implementation:

  | id | feature |
  |----|---------|
  | METRIC-EB-01 | A sloth-source preset reads budget and burn rates from synthetic Sloth-named recording rules and writes `status.errorBudget` |
  | METRIC-EB-02 | A gate on `slo.<name>.budgetRemaining` blocks prod while the budget is below 0 and opens when it recovers |
  | METRIC-EB-03 | A page-severity burn (both windows above 14.4) blocks with budget left; a short-window-only spike does not |
  | METRIC-EB-04 | Prometheus unreachable: the gate fails closed; with the bounded fail-open expression it passes, then blocks after the bound |
  | METRIC-EB-05 | `approval.requiredWhen` on a spent budget: an SRE approval releases a hotfix; the author's does not |
  | METRIC-EB-06 | A grouped check maps fleet targets to groups; one burning group holds only its target |
  | METRIC-EB-07 | Contract: real Sloth v0.16.0 and Pyrra v0.10.2 controllers generate the series the presets read (one SLO each, generic rules on for Pyrra) |

## 19. Work breakdown

Issues to open on milestone v0.14.0 when implementation starts (after v0.13.0 is tagged):

1. `feat(api): MetricCheck spec.errorBudget and status.errorBudget` (types, CRD rules, printer
   columns, envtest on 1.35). Risky PR.
2. `feat(metriccheck): error-budget preset queries for sloth, pyrra and prometheus sources`
   (query builder, vector reader, windows and factors, rounding, states of 7.1).
3. `feat(policygate): slo.* CEL variable, groups, unknownMinutes, wake-up predicate`
   (`docs/reference/cel-context.md` and its docs test).
4. `feat(policygate): fleet target to group resolution` (target name and target label).
5. `feat(cli,ui): budget in explain, why, simulate and the stage drawer`.
6. `docs: error budget gate page` (Sloth, Pyrra and plain-rule recipes, the Pyrra `web` recipe of
   3.2 (d), fail-open guidance, hotfix pattern, freeze interplay), changelog.
7. `test(e2e): METRIC-EB-01..07` with the coverage rows.
