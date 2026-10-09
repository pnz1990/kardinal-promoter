# CEL Context Reference

This page documents every variable available in `PolicyGate` CEL expressions. The context is built fresh on every evaluation using live CRD data.

Referencing an attribute or map key that does not exist is an evaluation error, and the gate blocks (fail-closed). A test (`TestDocumentedCELContext` in `pkg/reconciler/policygate`) evaluates every attribute and example on this page against the controller's real context.

Graph `readyWhen` and template expressions run in kro's CEL environment, not this one. The variables on this page exist only in PolicyGate expressions.

---

## Root Variables

| Variable | Type | Description |
|---|---|---|
| `bundle` | map | The Bundle being promoted |
| `schedule` | map | Current time context |
| `environment` | map | The target environment |
| `upstream` | map | Per-environment soak data from upstream |
| `metrics` | map | MetricCheck results from the gate's metrics namespace (the org policy namespace for an org gate, the gate's namespace otherwise) |
| `changewindow` | map | ChangeWindow name → `true` while the window is active (blocking); also `changewindow.isBlocked("name")` and `changewindow.isAllowed("name")`. An unknown name blocks the gate. See [ChangeWindow attributes](../policy-gates.md#changewindow-attributes-k-04) |
| `approvals` | map | The Approvals counted for this gate instance (`kardinal approve`). See [`approvals.*`](#approvals) |

---

## `bundle.*`

| Field | Type | Example | When populated |
|---|---|---|---|
| `bundle.type` | string | `"image"` | Always |
| `bundle.createdBy` | string | `"oidc:alice@example.com"` | The Bundle's verified creator (`kardinal.io/created-by`); `""` when it has none |
| `bundle.version` | string | `"1.29.0"` | Always. `image` and `mixed` Bundles: the first image's tag (`""` when that image has only a digest). `config` Bundles: the first 8 characters of `configRef.commitSHA` |
| `bundle.upstreamSoakMinutes` | int | `45` | Soak of the direct upstream environment(s) of the gated environment; minimum across them on fan-in; 0 for a root environment or an upstream that is not Verified |
| `bundle.provenance.author` | string | `"engineer@co.com"` | When Bundle was created with `provenance.author` |
| `bundle.provenance.commitSHA` | string | `"abc123def"` | When Bundle was created with `provenance.commitSHA` |
| `bundle.provenance.ciRunURL` | string | `"https://github.com/..."` | When Bundle was created with `provenance.ciRunURL` |
| `bundle.intent.targetEnvironment` | string | `"prod"` | Always; empty unless the Bundle sets `spec.intent.targetEnvironment` |
| `bundle.labels` | map | `{"hotfix": "true"}` | Always; the Bundle's `metadata.labels` (values are strings), empty map when none |
| `bundle.pr["<envName>"].isApproved` | bool | `true` | When a PRStatus exists for this Bundle and environment |
| `bundle.pr["<envName>"].approvalCount` | int | `2` | When a PRStatus exists for this Bundle and environment |

### Bundle examples

```cel
# Block bots from promoting to prod
bundle.provenance.author != "dependabot[bot]"

# Require upstream soak (shorthand for the direct upstream; minimum on fan-in)
bundle.upstreamSoakMinutes >= 30

# Block bundles labelled release-type=hotfix (bundles without the label pass)
!("release-type" in bundle.labels) || bundle.labels["release-type"] != "hotfix"

# Check intent
bundle.intent.targetEnvironment == "prod"
```

---

## `schedule.*`

| Field | Type | Example | Notes |
|---|---|---|---|
| `schedule.isWeekend` | bool | `false` | `true` on Saturday or Sunday (UTC) |
| `schedule.hour` | int | `14` | Hour of day in UTC (0–23) |
| `schedule.dayOfWeek` | string | `"Monday"` | Full weekday name (Monday, Tuesday, ..., Sunday) |

### Schedule examples

```cel
# Block weekend deploys
!schedule.isWeekend

# Block deploys outside business hours (9am–5pm UTC Mon–Fri)
!schedule.isWeekend && schedule.hour >= 9 && schedule.hour < 17

# Block Monday morning deploys (risky after weekend)
schedule.dayOfWeek != "Monday" || schedule.hour >= 10

# Allow only Friday deploys (release day)
schedule.dayOfWeek == "Friday"
```

---

## `environment.*`

| Field | Type | Example | Notes |
|---|---|---|---|
| `environment.name` | string | `"prod"` | The target environment this gate is evaluating for |

### Environment examples

```cel
# Block bots on prod only (allow on staging)
environment.name != "prod" || bundle.provenance.author != "dependabot[bot]"
```

---

## `upstream.*`

The `upstream` map contains per-environment data keyed by environment name. It is not limited to environments upstream of the gate: it has an entry for every environment in the Bundle's `status.environments` and in the history of the pipeline's last 10 Bundles. An environment with neither is absent, so referencing it is an evaluation error and the gate blocks.

| Field | Type | Example | Notes |
|---|---|---|---|
| `upstream.<envName>.soakMinutes` | int | `42` | This Bundle's soak minutes in that environment (from `Bundle.status.environments[].soakMinutes`; 0 when absent) |
| `upstream.<envName>.recentSuccessCount` | int | `3` | Verified promotions of that environment among the pipeline's last 10 Bundles |
| `upstream.<envName>.recentFailureCount` | int | `0` | Failed promotions of that environment among the pipeline's last 10 Bundles |
| `upstream.<envName>.lastPromotedAt` | string | `"2026-04-14T12:00:00Z"` | RFC3339 time of the last Verified promotion, `""` if none |

### Upstream examples

```cel
# Require 30 minutes of soak in uat before prod
upstream.uat.soakMinutes >= 30

# Require soak in both staging regions
upstream["staging-us"].soakMinutes >= 15 && upstream["staging-eu"].soakMinutes >= 15

# Shorthand: bundle.upstreamSoakMinutes is the soak of the direct upstream(s), minimum on fan-in
bundle.upstreamSoakMinutes >= 30
```

---

## `metrics.*`

The `metrics` map contains one entry per `MetricCheck` CRD in the gate's metrics namespace. Keys are MetricCheck names. For a gate made from a template in an org policy namespace (the controller's `--policy-namespaces`) that is the template's namespace, so the org's MetricChecks decide an org gate; for every other gate it is the gate's own namespace, the Pipeline namespace.

| Field | Type | Example | Notes |
|---|---|---|---|
| `metrics.<name>.value` | string | `"0.005"` | Last queried metric value, as a string; `""` after a query error or when the result is stale |
| `metrics.<name>.result` | string | `"Pass"` | `"Pass"` or `"Fail"` — result of the MetricCheck threshold; `"Stale"` when the result is stale |
| `metrics.<name>.stale` | bool | `false` | `true` when the MetricCheck's `status.validUntil` is unset (for example before its first evaluation) or earlier than the gate's evaluation time |

**Per-promotion MetricChecks.** For a MetricCheck with `spec.perPromotion: true`, `metrics.<name>` is the instance the Graph made from it for the gate's own Bundle and environment (labels `kardinal.io/metric-template`, `kardinal.io/bundle`, `kardinal.io/environment`). Until that instance exists, or when two objects claim to be it, the entry is stale. Instances are not listed under their own names. See [Per-promotion analysis](../metric-checks.md#per-promotion-analysis).

**Populated** when a `MetricCheck` CRD with the given name exists in the gate's metrics namespace. `double("")` is an evaluation error, so a value-based gate blocks until the MetricCheck has a fresh value.

**Staleness.** Each MetricCheck evaluation sets `status.validUntil` to the evaluation time plus
`max(3 × interval, 30s)`. A result the gate reads after that time, or one without `validUntil`, is
stale: `result` is `"Stale"`, `value` is `""` and `stale` is `true`, so `result == "Pass"` and
value comparisons block, and the gate's `status.reason` includes `metric "<name>" result is stale`.
Compare with `== "Pass"`, not `!= "Fail"`: a stale result is neither. `kardinal policy simulate` judges
staleness at the real current time, not at the simulated `--time`. See
[Stale metric results](../policy-gates.md#stale-metric-results).

### Metrics examples

```cel
# Block if error rate MetricCheck is failing
metrics["error-rate"].result == "Pass"

# Check raw value (convert string → double via standard CEL)
double(metrics["p99-latency"].value) < 500.0

# Allow if no MetricCheck exists (graceful degradation)
!("error-rate" in metrics) || metrics["error-rate"].result == "Pass"
```

---

## Extended CEL Functions

Gate expressions can use these functions in addition to standard CEL. They come from kardinal's `pkg/cel/library`, which is adapted from the [kro CEL library](https://github.com/kubernetes-sigs/kro/tree/main/pkg/cel/library). Only the functions listed here are available. Other kro functions, such as `hash.*`, `deepMerge` and `omit`, are not.

### JSON

| Function | Signature | Example |
|---|---|---|
| `json.marshal` | `(dyn) → string` | `json.marshal(bundle.provenance)` |
| `json.unmarshal` | `(string) → dyn` | `json.unmarshal('{"darkMode": true}').darkMode` |

### Maps

| Function | Signature | Example |
|---|---|---|
| `.merge()` | `map.merge(map) → map` | `bundle.labels.merge({"region": "us-east-1"})` |

> **Note**: `merge` is a **member function** called on a map: `map1.merge(map2)`.
> The namespace-style `maps.merge(map1, map2)` is incorrect and produces "undeclared reference" errors.

### Lists

| Function | Signature | Example |
|---|---|---|
| `lists.setAtIndex` | `(list, int, dyn) → list` | `lists.setAtIndex(["a", "b"], 0, "new-value")` |
| `lists.insertAtIndex` | `(list, int, dyn) → list` | `lists.insertAtIndex(["a", "b"], 0, "prepend")` |
| `lists.removeAtIndex` | `(list, int) → list` | `lists.removeAtIndex(["a", "b"], 0)` |

### Random (deterministic)

| Function | Signature | Example |
|---|---|---|
| `random.seededInt` | `(int, int, string) → int` | `random.seededInt(0, 100, bundle.version) < 10` |
| `random.seededString` | `(int, string) → string` | `random.seededString(8, bundle.version)` |

`random.seededInt(min, max, seed)` returns an integer from `min` up to, but not
including, `max`. `min` must be less than `max`. The same seed always gives the
same number.

`random.seededString` returns lowercase letters and digits. The length must be
between 1 and 1024. The same seed always gives the same string.

### Evaluation limits

Each evaluation of a gate expression has a runtime cost limit of 1,000,000 (the
per-expression limit Kubernetes uses for CRD validation rules) and a time limit
of 1 second. An expression that goes over either limit fails to evaluate, and
the gate blocks with a `CEL evaluation error` reason.

### String extensions

Standard `cel-go/ext` string functions are available:

| Method | Example |
|---|---|
| `.format(args)` | `"Bundle %s promoted".format([bundle.version])` |
| `.lowerAscii()` | `bundle.provenance.author.lowerAscii().contains("bot")` |

---

## Complete Expression Examples

```cel
# No weekend deploys
!schedule.isWeekend

# Business hours only (Mon–Fri 9am–5pm UTC)
!schedule.isWeekend && schedule.hour >= 9 && schedule.hour < 17

# Require 30-minute UAT soak
upstream.uat.soakMinutes >= 30

# Block bots on prod
bundle.provenance.author != "dependabot[bot]"

# Require low error rate from Prometheus MetricCheck
metrics["error-rate"].result == "Pass"

# Hotfix bypass: hotfix bundles skip soak requirement
("release-type" in bundle.labels && bundle.labels["release-type"] == "hotfix") || bundle.upstreamSoakMinutes >= 30

# Multi-condition prod gate
!schedule.isWeekend &&
schedule.hour >= 9 && schedule.hour < 17 &&
upstream.uat.soakMinutes >= 30 &&
metrics["error-rate"].result == "Pass"
```

---

## Simulating Expressions

Use `kardinal policy simulate` to evaluate an expression against the current context without creating a real Bundle:

```bash
kardinal policy simulate \
  --pipeline my-app \
  --env prod \
  --time "Saturday 3pm"
# RESULT: BLOCKED
# Blocked by: no-weekend-deploys
# Message: "Production deployments are blocked on weekends"
# Next window: Monday 00:00 UTC
#
# no-weekend-deploys:   BLOCK   (!schedule.isWeekend = false)

kardinal policy simulate \
  --pipeline my-app \
  --env prod \
  --time "Tuesday 10am"
# RESULT: PASS
# no-weekend-deploys:   PASS   (!schedule.isWeekend = true)
```

Simulate evaluates the same gates the controller attaches to that environment
(team gates in the pipeline namespace and org gates in the policy namespaces),
with the controller's CEL environment. `--time` is UTC and accepts an RFC 3339
timestamp or a weekday and an hour ("Saturday 3pm", "tue 10:30"); it defaults to
now, and anything else is an error.

Each blocked gate is listed with its `spec.message` (the expression's result
when it has none) and, when a later hour within a week passes it, that hour as
the next window. Then every gate gets a row, PASS or BLOCK, with what its
expression evaluated to. Simulate exits 0 whether the result is PASS or
BLOCKED.

---

## Testing Expressions

Validate CEL syntax and semantics before deploying:

```bash
kardinal policy test my-gate.yaml
# PolicyGate "no-weekend-deploys" (my-gate.yaml):
#   Expression: !schedule.isWeekend
#   Syntax: valid
#   Result: PASS (!schedule.isWeekend = true)
#
# All gates valid and pass current context (1 gate(s))
```

---

## See Also

- [Policy Gates](../policy-gates.md) — PolicyGate CRD reference
- [CLI Reference: policy simulate](cli/kardinal-policy-simulate.md) — simulate gate evaluation

---

## `approvals.*`

The decisions `kardinal approve` recorded for the gate instance's Bundle and environment (the Graph copies them into `spec.approvals`). With `spec.approval` set, only allowed approvers count (see [Approval gates](../policy-gates.md#approval-gates)); without it, every approval counts. Each user counts once.

| Field | Type | Example | When populated |
|---|---|---|---|
| `approvals.count` | int | `2` | Always; `0` with no approval |
| `approvals.required` | int | `2` | `spec.approval.required`; `0` without `spec.approval` |
| `approvals.users` | list | `["alice@example.com"]` | Always; the users whose approval counts, sorted |
| `approvals.rejected` | bool | `false` | Always; `true` when a counted user rejected |

```yaml
# Two approvals, outside the weekend
expression: 'approvals.count >= 2 && !approvals.rejected && !schedule.isWeekend'
```

