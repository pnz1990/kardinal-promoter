# Policy Gates

PolicyGates are CEL-powered policy checks that block promotions until their conditions are met. They are represented as nodes in the promotion DAG, visible in the UI and inspectable via `kardinal explain`.

## How PolicyGates Work

1. A platform team defines PolicyGate CRDs in the `platform-policies` namespace (org-level) or in a team namespace (team-level).
2. When a Bundle is created and a Graph is generated from the Pipeline, the controller collects all matching PolicyGates and injects them as nodes in the Graph between the upstream environment and the target environment.
3. The Graph controller creates per-Bundle PolicyGate instances.
4. The kardinal-controller evaluates each instance's CEL expression against the current promotion context.
5. If the expression evaluates to `true`, the gate passes and Graph advances. If `false`, the gate blocks and downstream PromotionSteps wait.

## PolicyGate CRD

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: <string>
  namespace: <string>                   # platform-policies (org) or team namespace
  labels:
    kardinal.io/scope: <string>         # "org" or "team"
    kardinal.io/applies-to: <string>    # comma-separated environment names
    kardinal.io/type: <string>          # "gate" (default) or "skip-permission"
spec:
  expression: <string>                  # CEL expression
  message: <string>                     # human-readable explanation shown when gate blocks
  recheckInterval: <duration>           # how often to re-evaluate (default: "5m")
```

## Scoping

### Org-level gates

Created in the `platform-policies` namespace (configurable via `--policy-namespaces` controller flag). Org gates are mandatory: they are injected into every Pipeline that targets the matching environment. Teams cannot remove them.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend-deploys
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
    kardinal.io/type: gate
spec:
  expression: "!schedule.isWeekend"
  message: "Production deployments are blocked on weekends"
  recheckInterval: 5m
```

### Team-level gates

Created in the team's own namespace. Team gates are additive: they are injected alongside org gates. Teams can add restrictions but cannot bypass org gates.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-bot-deploys-to-prod
  namespace: my-team
  labels:
    kardinal.io/scope: team
    kardinal.io/applies-to: prod
    kardinal.io/type: gate
spec:
  expression: 'bundle.provenance.author != "dependabot[bot]"'
  message: "Automated dependency updates must be manually promoted to prod"
  recheckInterval: 5m
```

### Matching

The `kardinal.io/applies-to` label determines which environments a gate blocks. It supports comma-separated values:

```yaml
labels:
  kardinal.io/applies-to: prod                    # blocks "prod" only
  kardinal.io/applies-to: prod-us,prod-eu          # blocks both prod regions
  kardinal.io/applies-to: staging,prod             # blocks staging and prod
```

The controller scans:
1. The `--policy-namespaces` flag namespaces (default: `platform-policies`)
2. The Pipeline's own namespace

All matching PolicyGates from both sources are injected into the Graph.

## CEL Context

All PolicyGate expressions are evaluated against the following context. All attributes listed are available in the current release.

### Core attributes

| Attribute | Type | Description | Example |
|---|---|---|---|
| `bundle.version` | string | Image tag or semver | `"1.29.0"` |
| `bundle.labels.*` | map[string]string | Bundle labels | `bundle.labels.hotfix == true` |
| `bundle.provenance.author` | string | Who triggered the CI build | `"dependabot[bot]"` |
| `bundle.provenance.commitSHA` | string | Source commit | `"abc123"` |
| `bundle.provenance.ciRunURL` | string | CI run link | `"https://..."` |
| `bundle.intent.target` | string | Target environment | `"prod"` |
| `schedule.isWeekend` | bool | Saturday or Sunday | `false` |
| `schedule.hour` | int | Hour in UTC (0-23) | `14` |
| `schedule.dayOfWeek` | string | Day name | `"Tuesday"` |
| `environment.name` | string | Target environment name | `"prod"` |
| `environment.approval` | string | Approval mode | `"pr-review"` |

### Metric and soak attributes

| Attribute | Type | Description |
|---|---|---|
| `metrics.*` | float64 | MetricCheck results injected by name (requires a `MetricCheck` CRD targeting this environment) |
| `bundle.upstreamSoakMinutes` | int | Minutes since upstream environment was verified |
| `previousBundle.version` | string | Previously deployed version in this environment |

### Planned attributes (not yet available)

!!! warning "Not yet implemented"
    The following attributes are on the roadmap but will cause a CEL evaluation error if referenced today.
    Gates using them will fail closed.

| Attribute | Type | Description |
|---|---|---|
| `delegation.status` | string | Argo Rollouts or Flagger rollout status |
| `externalApproval.*` | map | Webhook gate response data |

## CEL Expression Examples

### Time-based

```yaml
# Block weekends
expression: "!schedule.isWeekend"

# Block outside business hours (9am-5pm UTC)
expression: "schedule.hour >= 9 && schedule.hour < 17"

# Block Fridays after 3pm
expression: '!(schedule.dayOfWeek == "Friday" && schedule.hour >= 15)'
```

### Bundle-based

```yaml
# Require a minimum soak time in the upstream environment
expression: "bundle.upstreamSoakMinutes >= 30"

# Block automated dependency updates from reaching prod
expression: 'bundle.provenance.author != "dependabot[bot]"'

# Only allow bundles with a hotfix label
expression: "bundle.labels.hotfix == true"

# Block if the target is too many major versions ahead
expression: 'bundle.version.startsWith("1.")'
```

### Metric-based

```yaml
# Require 99.5% success rate in the upstream environment
expression: "metrics.successRate >= 0.995"

# Block if latency is too high
expression: "metrics.p99LatencyMs < 500"
```

### Composite

```yaml
# Business hours AND not a bot AND soak time passed
expression: |
  schedule.hour >= 9 &&
  schedule.hour < 17 &&
  !schedule.isWeekend &&
  bundle.provenance.author != "dependabot[bot]" &&
  bundle.upstreamSoakMinutes >= 30
```

## Skip Permissions

When a Bundle's `intent.skip` lists an environment, the controller checks whether the skip is permitted. If any org-level PolicyGate applies to the skipped environment, the skip is denied unless a SkipPermission PolicyGate explicitly allows it.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-staging-skip-for-hotfix
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: staging
spec:
  expression: "bundle.labels.hotfix == true"
  message: "Hotfix bundles may skip staging"
```

Skip permissions are evaluated synchronously before the Graph is created. If denied, the Bundle's status is set to `SkipDenied` with a reason message. The Bundle does not promote.

Without any SkipPermission PolicyGate, skipping an environment that has org gates is always denied.

## Re-evaluation

PolicyGates are re-evaluated periodically at the configured `recheckInterval`. This is necessary for time-based gates (e.g., weekend checks, soak time) where the gate may become passing without any cluster state change.

The controller writes `status.lastEvaluatedAt` on each re-evaluation. The Graph's `readyWhen` includes a freshness check. If the controller restarts and has not yet re-evaluated a gate, `lastEvaluatedAt` will be stale and Graph treats the gate as not-ready until the controller catches up.

## Mid-Flight Policy Changes

PolicyGates are injected into the Graph at Graph creation time (when the Bundle starts promoting). If a new org-level PolicyGate is added while a Bundle is mid-flight, it does not apply to that Bundle's existing Graph. It applies to all subsequent Bundles.

To block an in-flight promotion, use `kardinal pause <pipeline>`, which injects a freeze gate that takes effect immediately.

## Inspecting PolicyGates

```bash
# List all PolicyGates
kardinal policy list

# See which gates are blocking a promotion
kardinal explain my-app --env prod

# Validate a PolicyGate file
kardinal policy test my-gate.yaml

# Simulate a gate against hypothetical conditions
kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"
```
