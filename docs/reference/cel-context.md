# CEL Context Reference

This page documents every variable available in `PolicyGate` CEL expressions. The context is built fresh on every evaluation using live CRD data.

---

## Root Variables

| Variable | Type | Description |
|---|---|---|
| `bundle` | map | The Bundle being promoted |
| `schedule` | map | Current time context |
| `environment` | map | The target environment |
| `upstream` | map | Per-environment soak data from upstream |
| `metrics` | map | MetricCheck results from the same namespace |

---

## `bundle.*`

| Field | Type | Example | When populated |
|---|---|---|---|
| `bundle.type` | string | `"image"` | Always |
| `bundle.version` | string | `"1.29.0"` | Always; derived from image tag or configRef |
| `bundle.upstreamSoakMinutes` | int | `45` | After any upstream environment is Verified; max across all envs |
| `bundle.provenance.author` | string | `"engineer@co.com"` | When Bundle was created with `provenance.author` |
| `bundle.provenance.commitSHA` | string | `"abc123def"` | When Bundle was created with `provenance.commitSHA` |
| `bundle.provenance.ciRunURL` | string | `"https://github.com/..."` | When Bundle was created with `provenance.ciRunURL` |
| `bundle.intent.targetEnvironment` | string | `"prod"` | When Bundle has `spec.intent.targetEnvironment` set |
| `bundle.metadata.annotations` | map | `{"team": "platform"}` | Available via `bundle.metadata.annotations["key"]` |

### Bundle examples

```cel
# Block bots from promoting to prod
bundle.provenance.author != "dependabot[bot]"

# Require upstream soak (convenience shorthand — max across all upstream envs)
bundle.upstreamSoakMinutes >= 30

# Block hotfix bundles from skipping gates
bundle.metadata.annotations["release-type"] != "hotfix"

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

The `upstream` map contains per-environment data for all environments that have been promoted **upstream** of the current gate's environment. Keys are environment names.

| Field | Type | Example | Notes |
|---|---|---|---|
| `upstream.<envName>.soakMinutes` | int | `42` | Minutes since the Bundle was Verified in that environment |

**Only populated** for environments that have status `Verified` in `Bundle.status.environments`.

### Upstream examples

```cel
# Require 30 minutes of soak in uat before prod
upstream.uat.soakMinutes >= 30

# Require soak in both staging regions
upstream["staging-us"].soakMinutes >= 15 && upstream["staging-eu"].soakMinutes >= 15

# Convenience: use bundle.upstreamSoakMinutes for max across all upstream envs
bundle.upstreamSoakMinutes >= 30
```

---

## `metrics.*`

The `metrics` map contains one entry per `MetricCheck` CRD in the same namespace as the gate. Keys are MetricCheck names.

| Field | Type | Example | Notes |
|---|---|---|---|
| `metrics.<name>.value` | string | `"0.005"` | Last queried metric value (as string) |
| `metrics.<name>.result` | string | `"pass"` | `"pass"` or `"fail"` — result of the MetricCheck threshold |

**Populated** when a `MetricCheck` CRD with the given name exists in the gate's namespace and has been evaluated by the MetricCheckReconciler.

### Metrics examples

```cel
# Block if error rate MetricCheck is failing
metrics["error-rate"].result == "pass"

# Check raw value (convert string → float via standard CEL)
double(metrics["p99-latency"].value) < 500.0

# Allow if no MetricCheck exists (graceful degradation)
!has(metrics.error_rate) || metrics["error-rate"].result == "pass"
```

---

## Extended CEL Functions (kro library)

kardinal uses the [kro CEL library](https://github.com/kubernetes-sigs/kro/tree/main/pkg/cel/library) for all gate evaluation. These functions are available in addition to standard CEL:

### JSON

| Function | Signature | Example |
|---|---|---|
| `json.marshal` | `(dyn) → string` | `json.marshal(bundle.provenance)` |
| `json.unmarshal` | `(string) → dyn` | `json.unmarshal(bundle.metadata.annotations["config"]).featureFlags.darkMode` |

### Maps

| Function | Signature | Example |
|---|---|---|
| `maps.merge` | `(map, map) → map` | `maps.merge(environment, {"region": "us-east-1"})` |

### Lists

| Function | Signature | Example |
|---|---|---|
| `lists.setAtIndex` | `(list, int, dyn) → list` | `lists.setAtIndex(myList, 0, "new-value")` |
| `lists.insertAtIndex` | `(list, int, dyn) → list` | `lists.insertAtIndex(myList, 0, "prepend")` |
| `lists.removeAtIndex` | `(list, int) → list` | `lists.removeAtIndex(myList, 0)` |

### Random (deterministic)

| Function | Signature | Example |
|---|---|---|
| `random.seededInt` | `(int, int, int) → int` | `random.seededInt(0, 100, bundle.version.hashCode()) < 10` |

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
metrics["error-rate"].result == "pass"

# Hotfix bypass: hotfix bundles skip soak requirement
bundle.metadata.annotations["release-type"] == "hotfix" || bundle.upstreamSoakMinutes >= 30

# Multi-condition prod gate
!schedule.isWeekend &&
schedule.hour >= 9 && schedule.hour < 17 &&
upstream.uat.soakMinutes >= 30 &&
metrics["error-rate"].result == "pass"
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
# no-weekend-deploys: !schedule.isWeekend evaluated to false (isWeekend=true)

kardinal policy simulate \
  --pipeline my-app \
  --env prod \
  --time "Tuesday 10am"
# RESULT: ALLOWED
# no-weekend-deploys: !schedule.isWeekend evaluated to true
```

---

## Testing Expressions

Validate CEL syntax and semantics before deploying:

```bash
kardinal policy test --file my-gate.yaml
# PASS: expression "!schedule.isWeekend && upstream.uat.soakMinutes >= 30" is valid CEL
```

---

## See Also

- [Policy Gates](../policy-gates.md) — PolicyGate CRD reference
- [CLI Reference: policy simulate](../cli-reference.md#kardinal-policy-simulate) — simulate gate evaluation
- [AGENTS.md CEL section](https://github.com/pnz1990/kardinal-promoter/blob/main/AGENTS.md) — kro library function catalog
