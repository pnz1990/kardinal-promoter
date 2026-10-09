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
  name: <string>                        # at most 63 characters
  namespace: <string>                   # platform-policies (org) or team namespace
  labels:
    kardinal.io/scope: <string>         # "org" or "team"
    kardinal.io/applies-to: <string>    # one environment name
    kardinal.io/type: <string>          # "gate" (default) or "skip-permission"
spec:
  expression: <string>                  # CEL expression
  message: <string>                     # why the gate blocks, shown when its expression is false
  recheckInterval: <duration>           # how often to re-evaluate (default: "5m", minimum: "10s")
  when: <string>                        # deprecated, has no effect (see below)
```

A gate's name is at most 63 characters, because kardinal copies it into the
`kardinal.io/gate-template` label of every gate instance. The API server rejects a longer name.
Only the PolicyGates kardinal creates can have longer names: the per-Bundle gate instances and the
`freeze-<pipeline>` gate of a paused Pipeline. Kardinal sets `spec.generated: true` on them. Do not
set it on a gate you write: kardinal never uses a gate with `spec.generated` as a template, so the
gate would not apply to any environment.

### What users see when a gate blocks

When a gate's expression is false, its `status.reason` starts with `spec.message` and ends with the
evaluated result in parentheses:

```
Production deployments are blocked on weekends (bundle.version=1.29.0: !schedule.isWeekend = false)
```

The gate's `Ready` condition, `kardinal explain` and the UI show this reason. `kardinal status
<pipeline>` shows the message alone under Blocking Policy Gates. The Pending step's message names
the gate instance and the gate's message: `waiting for gate
no-weekend-deploys-platform-policies-prod--<bundle>: Production deployments are blocked on
weekends`. An instance name that is too long, or has characters a name cannot hold, ends in a hash. A gate without a message shows only the result (`bundle.version=1.29.0:
!schedule.isWeekend = false`). The message is not shown when the gate blocks for another reason: an
evaluation error (`bundle.version=1.29.0: CEL evaluation error: ...`) or a context error
(`context error: ...`) keeps its own reason, because the message does not explain it.

### When a gate holds a step

Every gate on an environment holds that environment back, twice:

1. The Graph does not create the environment's PromotionStep until every gate on it reports ready.
2. Right before the step starts, before any git operation, the PromotionStep reconciler re-checks
   every gate in the step's `spec.requiredGates`. The step starts only when each gate exists, is
   ready, and was evaluated at or after the step was created (its `status.lastEvaluatedAt` is not
   earlier than the step's `creationTimestamp`). Otherwise the step stays in `Pending` with the
   message `waiting for gate <instance>` (`waiting for gate <instance>: <message>` when the gate's
   expression is false and it has a message) or `waiting for gate <instance> to be re-evaluated`.
   Nothing is pushed and no PR is opened.

The Graph acts on the gate's last result, which can be older than the step: it can predate a
metric, change window or schedule change. So the controller re-evaluates a new step's gates as soon
as the step is created, and a gate that is still true lets the step start within seconds.

While a gate that is not ready holds a step, `kardinal status` lists it under Blocking Policy Gates
and the UI counts it as a blocker (the Blocked label and the Blockers column). A ready gate whose
result is older than the step still shows as ready while the step waits for its re-evaluation; the
step's message says which gate it waits for.

Limits:

- **A step that has started is not stopped.** Once the step leaves `Pending`, a gate that turns
  false does not interrupt it: gates are not evaluated against a deployment in progress. Use `bake`
  for post-deployment soak and `health` for health checks.
- **Steps wait if gates cannot be evaluated.** A step that needs a fresh result waits until the
  controller writes one (it fails closed).
- **Clock skew.** The API server sets the step's `creationTimestamp`; the controller's clock sets
  the gate's `lastEvaluatedAt`. Both have one-second precision, so a result from the same second as
  the step counts. If the controller's clock is behind the API server's, a fresh result can look
  older than the step, and the step waits for a later evaluation (the next ScheduleClock tick or
  `recheckInterval` after the skew has passed). If the controller's clock is ahead, a result up to
  the skew old is accepted. Keep node clocks synchronized. This effect is not covered by tests.

### `when` field (deprecated)

`spec.when` has no effect and is deprecated: `pre-deploy` and `post-deploy` behave the same, and
every gate is re-checked before its step starts, as described above. The field keeps its values and
its default (`post-deploy`), so existing manifests still apply. `kardinal validate` warns when it is
set. Remove it from your gates.

**Example: block prod deployments when staging error rate is high**

```yaml
kind: PolicyGate
metadata:
  name: staging-healthy-before-prod
  namespace: platform-policies
spec:
  expression: 'double(metrics["staging-error-rate"].value) < 0.01'
  message: "Staging error rate is above 1% — do not start prod deployment"
  recheckInterval: 1m
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

Created in the team's own namespace. Team gates are additive: they are injected alongside org gates. A team Pipeline can add restrictions but cannot remove or weaken org gates. The escape hatch is [`kardinal override`](#emergency-overrides-k-09): it patches the gate instances in the Pipeline namespace, so anyone with `patch` on PolicyGates there can force-pass an org gate for a limited time, with the reason recorded. Grant that verb only to the people allowed to break glass.

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

The `kardinal.io/applies-to` label names the one environment a gate blocks:

```yaml
labels:
  kardinal.io/applies-to: prod                    # blocks "prod" only
```

A label value cannot contain a comma, so the API server rejects a list such as
`prod-us,prod-eu`. To block several environments, create one PolicyGate per environment,
each with its own `kardinal.io/applies-to` value.

The controller reads gates from these places:
1. The `--policy-namespaces` flag namespaces (default: `platform-policies`). These are the org policy namespaces.
2. The Pipeline's own namespace.
3. Any namespaces listed in the Pipeline's `spec.policyNamespaces`.

Every matching PolicyGate from all three is added to the Graph. `spec.policyNamespaces` can only add namespaces; the org policy namespaces are read whatever it says. A gate from the Pipeline's namespace or from `spec.policyNamespaces` is a team gate, unless it is labelled `kardinal.io/scope: org`. Either way, it can never grant a skip (see [Skip Permissions](#skip-permissions)).

The Graph creates every gate instance in the Pipeline namespace and labels it
`kardinal.io/gate-template-namespace` with its template's namespace. A gate from an org policy
namespace reads `metrics.*` from the MetricChecks of that namespace, so the platform team owns
the MetricChecks its gates use and a team MetricCheck of the same name cannot decide them. Every
other gate reads the MetricChecks of the Pipeline namespace. An instance created before this
label existed reads the Pipeline namespace until its Bundle's Graph is applied again.

A team can edit the labels of the instances in its namespace, so the controller trusts the label
only when the namespace it names is an org policy namespace and holds the PolicyGate that the
instance's `kardinal.io/gate-template` label names. Otherwise the instance reads the Pipeline
namespace, where a metric it names but cannot find still blocks it. If the controller cannot read
the template, the gate evaluates with no metrics, so an expression that reads `metrics.*` blocks.

## CEL Context

All PolicyGate expressions are evaluated against the following context. All attributes listed are available in the current release; a test evaluates every attribute and example on this page against the controller's real context. Referencing an attribute or map key that does not exist is an evaluation error, and the gate blocks (fail-closed). See the [CEL context reference](reference/cel-context.md) for the full list.

### Core attributes

| Attribute | Type | Description | Example |
|---|---|---|---|
| `bundle.type` | string | Bundle type | `"image"` |
| `bundle.version` | string | Image tag or semver | `"1.29.0"` |
| `bundle.labels` | map | Bundle `metadata.labels`; values are strings. Always a map (empty when the Bundle has no labels) | `has(bundle.labels.hotfix) && bundle.labels.hotfix == "true"` |
| `bundle.provenance.author` | string | Who triggered the CI build | `"dependabot[bot]"` |
| `bundle.provenance.commitSHA` | string | Source commit | `"abc123"` |
| `bundle.provenance.ciRunURL` | string | CI run link | `"https://..."` |
| `bundle.intent.targetEnvironment` | string | `spec.intent.targetEnvironment` (empty when unset) | `"prod"` |
| `schedule.isWeekend` | bool | Saturday or Sunday | `false` |
| `schedule.hour` | int | Hour in UTC (0-23) | `14` |
| `schedule.dayOfWeek` | string | Day name | `"Tuesday"` |
| `environment.name` | string | Target environment name | `"prod"` |

### Metric and soak attributes

| Attribute | Type | Description |
|---|---|---|
| `metrics.<name>.value` | string | Last value of the `MetricCheck` named `<name>` in the gate's [metrics namespace](#matching) (the org policy namespace for an org gate, the Pipeline namespace otherwise), as a string (`""` after a query error or when the result is stale). Convert with `double(...)` |
| `metrics.<name>.result` | string | `"Pass"` or `"Fail"`: the MetricCheck's own threshold result. `"Stale"` when the result is stale |
| `metrics.<name>.stale` | bool | `true` when the MetricCheck's result has not been refreshed in time: its `status.validUntil` is unset or has passed |
| `bundle.upstreamSoakMinutes` | int | Soak minutes of the environment(s) directly upstream of the gated environment. With several direct upstreams (fan-in) it is the minimum. An upstream that is not Verified counts as 0. A root environment gets 0. |

MetricChecks query Prometheus, Datadog, CloudWatch, New Relic or any JSON web API, with credentials from Secrets; a `perPromotion` MetricCheck is instantiated for each Bundle and environment with the Bundle's version in its query, and the gate of that Bundle reads its own instance as `metrics.<name>` (see [Metric Checks](metric-checks.md)).

The controller does not query a MetricCheck URL on a loopback, link-local or cloud metadata address: the MetricCheck's `status.reason` then reads `destination address is not allowed` (see [Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls)).

#### Stale metric results

Each MetricCheck evaluation writes `status.validUntil`: the evaluation time plus three times the
MetricCheck's `interval`, and at least 30 seconds (3 minutes with the default `interval: 1m`). A
gate compares it with the time of its own evaluation. When `validUntil` is unset or has passed, the
result is stale: nothing has refreshed it for three intervals, for example after a controller
outage or while the MetricCheck's status write keeps failing (the controller evaluates again and
retries the write after 5 seconds the first time, then every `interval`, and the first write that
works refreshes the result; a write that fails on an evaluation an edit or a controller restart
started, less than an `interval` after the last write that worked, is retried at `interval`). A
stale result is exposed as
`result: "Stale"`, `value: ""` and `stale: true`, so both `metrics["x"].result == "Pass"` and
`double(metrics["x"].value) < 0.01` block (the second with an evaluation error, since `""` is not a
number). Compare with `== "Pass"`, not `!= "Fail"`: a stale result is neither. The gate's
`status.reason` then includes `metric "x" result is stale`, after the evaluated result (inside the
parentheses when the gate has a message). `kubectl get metriccheck x -o yaml` shows
`lastEvaluatedAt` and `validUntil`.

A stale result is noticed at the gate's next evaluation (the next ScheduleClock tick or
`recheckInterval`), and the first evaluation that refreshes it re-evaluates the gate at once.
Right after an upgrade from a version without `validUntil`, every MetricCheck is stale until its
first evaluation. The controller evaluates all of them when it starts, so metric gates can hold for
at most one `interval`.

### Cross-stage history attributes (K-10)

Available for all gates. The history is computed from Bundle CRD status across the last 10
promotions for the pipeline — no external API calls. The lookup is scoped to the last 10
Bundles by creation time. `upstream` has an entry for every environment in this Bundle's
status or in that history (not only the gated environment's upstreams); any other name is an
evaluation error and the gate blocks.

| Attribute | Type | Description |
|---|---|---|
| `upstream.<env>.recentSuccessCount` | int | Number of bundles with `Verified` status for `<env>` in the last 10 promotions |
| `upstream.<env>.recentFailureCount` | int | Number of bundles with `Failed` status for `<env>` in the last 10 promotions |
| `upstream.<env>.lastPromotedAt` | string | RFC3339 timestamp of the last successful promotion for `<env>` (empty string if never) |
| `upstream.<env>.soakMinutes` | int | This Bundle's soak minutes in `<env>` (from `Bundle.status.environments[].soakMinutes`; 0 when the Bundle has no status for `<env>`) |

### PR review attributes (K-08)

Available when the stage has `approval: pr-review`. The review state is written by the
PRStatus controller to `PRStatus.status.approved` / `status.approvalCount` — no
external API calls are made during gate evaluation.

| Attribute | Type | Description |
|---|---|---|
| `bundle.pr["<stageName>"].isApproved` | bool | True when the PR for the named stage has at least one approval and no outstanding change-request reviews |
| `bundle.pr["<stageName>"].approvalCount` | int | Number of distinct approving reviews on the stage PR |

Examples:

```yaml
# Block prod until staging has been successfully promoted at least 3 times recently
expression: 'upstream.staging.recentSuccessCount >= 3'

# Block if staging recently had 2+ failures (quality gate)
expression: 'upstream.staging.recentFailureCount < 2'

# Block if staging has never been promoted
expression: 'upstream.staging.lastPromotedAt != ""'

# Composite: soak + recent success history
expression: |
  upstream.staging.soakMinutes >= 60 &&
  upstream.staging.recentSuccessCount >= 3 &&
  !changewindow["holiday-freeze"]

# Block until the staging PR is approved
expression: 'bundle.pr["staging"].isApproved'

# Require at least 2 approvers before promoting to prod
expression: 'bundle.pr["staging"].approvalCount >= 2'

# Combine PR review with time gate
expression: '!schedule.isWeekend && bundle.pr["staging"].isApproved'
```

When no PRStatus exists for the named stage (e.g. the `open-pr` step has not run yet),
`bundle.pr` has no `"staging"` key, so `bundle.pr["staging"].isApproved` is an evaluation
error and the gate blocks (fail-closed). To treat a missing PR as "not approved" without an
error, write `"staging" in bundle.pr && bundle.pr["staging"].isApproved`.

### ChangeWindow attributes (K-04)

A `ChangeWindow` is a cluster-scoped object that describes when promotions are blocked.
It blocks nothing by itself: a PolicyGate blocks while a window it references is active.
Reference windows from an org-level gate to freeze every pipeline with one object.

```yaml
# A one-off freeze: active from start (inclusive) to end (exclusive)
apiVersion: kardinal.io/v1alpha1
kind: ChangeWindow
metadata:
  name: q4-holiday-freeze        # cluster-scoped: no namespace
spec:
  type: blackout
  start: "2026-12-20T00:00:00Z"
  end: "2027-01-02T00:00:00Z"
  reason: "Q4 holiday freeze"
---
# A recurring allowed window: active (blocking) outside Mon-Fri 09:00-17:00 in Los Angeles
apiVersion: kardinal.io/v1alpha1
kind: ChangeWindow
metadata:
  name: business-hours
spec:
  type: recurring
  schedule:
    timezone: America/Los_Angeles  # IANA name, default UTC
    allowedDays: [Mon, Tue, Wed, Thu, Fri]  # empty = every day
    allowedHours: "09:00-17:00"    # HH:MM-HH:MM, end exclusive; "24:00" allowed as end; empty = all day
```

For `recurring`, `schedule` lists when promotions are **allowed**; the window is active
(blocking) at every other time. An overnight range such as `"22:00-02:00"` belongs to the
day it starts on. A ChangeWindow with an invalid spec (end not after start, an unknown
timezone, `timezone: Local`, an unknown day name) is treated as active, so gates that
reference it block. Its `Valid` condition is `False` with reason `InvalidSpec` and a message
that names the problem; `kubectl get changewindows` shows `VALID`, and `-o wide` adds the reason.
The controller reports each invalid generation of the spec once, with a warning log line and,
in cluster mode, an `InvalidSpec` Warning Event on the ChangeWindow (in the `default`
namespace, because ChangeWindows are cluster-scoped). The
timezone database is compiled into the controller, so any IANA name works without tzdata in
the image.

The controller writes `status.active` and `status.reason` at every window boundary, and that
write re-evaluates the gates that reference a window. Gates evaluate the window spec at their
own evaluation time, so they never rely on a stale status.

The `changewindow` variable maps each ChangeWindow name to whether it is active. Two
equivalent syntaxes are available:

| Syntax | Returns | Description |
|---|---|---|
| `changewindow["window-name"]` | bool | `true` when the window is currently active (blocking) |
| `changewindow.isBlocked("window-name")` | bool | Same as above — method-call alias |
| `changewindow.isAllowed("window-name")` | bool | `true` when the window is NOT active |

Gates fail closed:

- If the named window does not exist, every syntax is an evaluation error and the gate blocks:
  `changewindow: unknown ChangeWindow "<name>"` for `isBlocked` and `isAllowed`, and
  `no such key: <name>` for `changewindow["<name>"]`. A typo or a deleted window never allows a
  promotion.
- If the controller cannot list ChangeWindows (for example missing RBAC), gates that
  reference `changewindow` block with `context error: changewindow: list ChangeWindows: ...`.

Examples:

```yaml
# Block prod during Q4 holiday freeze
expression: '!changewindow.isBlocked("q4-holiday-freeze")'

# Equivalent legacy syntax
expression: '!changewindow["q4-holiday-freeze"]'

# Only promote inside business hours, and never during the freeze
expression: 'changewindow.isAllowed("business-hours") && !changewindow.isBlocked("q4-holiday-freeze")'
```

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

# Only allow bundles with a hotfix label (label values are strings)
expression: 'has(bundle.labels.hotfix) && bundle.labels.hotfix == "true"'

# Block if the target is too many major versions ahead
expression: 'bundle.version.startsWith("1.")'
```

### Metric-based

```yaml
# Require 99.5% success rate (MetricCheck "success-rate"; value is a string)
expression: 'double(metrics["success-rate"].value) >= 0.995'

# Block while the "p99-latency" MetricCheck's own threshold fails
expression: 'metrics["p99-latency"].result == "Pass"'
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

# PR review AND time gate — 2+ approvals required on business days
expression: |
  !schedule.isWeekend &&
  bundle.pr["staging"].approvalCount >= 2
```

## Skip Permissions

A Bundle can skip environments with `intent.skipEnvironments`. When an org-level gate applies to a skipped environment, the skip needs a skip-permission gate. The permission counts only if it meets all of these conditions:

- it is labelled `kardinal.io/type: skip-permission`;
- it sets `spec.skipPermission: true`;
- it applies to the skipped environment through `kardinal.io/applies-to`;
- it lives in an org policy namespace (`--policy-namespaces`, default `platform-policies`).

A gate in a team namespace can never grant a skip, even if it is labelled `kardinal.io/scope: org`. The same holds for a namespace a Pipeline adds with `spec.policyNamespaces`.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-staging-skip-for-hotfix
  namespace: platform-policies
  labels:
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: staging
spec:
  skipPermission: true
  expression: 'bundle.version.startsWith("hotfix-")'
  message: "Only hotfix bundles may skip staging"
```

The controller checks for the permission when it builds the Bundle's Graph. If no permission exists, the Bundle goes to phase `Failed`, and its status conditions give the reason: `skip denied for environment "staging": ...`. The Bundle does not promote.

If a permission exists, the controller evaluates its expression like any other gate. It creates an instance of the permission gate in front of the next environment the Bundle promotes. That environment, `prod` in a `test → staging → prod` pipeline, waits until the expression is true. It never starts for a Bundle the permission does not cover. `kardinal explain` shows the instance with the other gates of that environment. The instance is annotated `kardinal.io/skipped-environments: staging`. If the skipped environment is the last one, nothing comes after it, so nothing waits on the permission.

Without a skip-permission gate, skipping an environment that has org gates is always denied. An environment that only team gates apply to can be skipped without a permission.

## Re-evaluation

PolicyGates are re-evaluated when any of the following occurs:

1. **ScheduleClock tick** (primary mechanism) — A `ScheduleClock` object in `kardinal-system`
   writes `status.tick` every minute by default. The PolicyGate reconciler watches all `ScheduleClock`
   objects; each tick triggers re-evaluation of all active PolicyGate instances cluster-wide
   (not those of [finished Bundles](#gates-of-finished-bundles)). Creating, editing or deleting a clock does not.
   This is the recommended pattern for time-based gates (`schedule.isWeekend`, `schedule.hour`, etc.).

2. **`recheckInterval`** — Each gate is also re-evaluated every `recheckInterval`, whether or not
   a `ScheduleClock` is installed. Without a `ScheduleClock` this is the only periodic
   re-evaluation. The minimum is `10s`: a smaller value is raised to `10s`.

3. **MetricCheck result change** — When a `MetricCheck`'s result or value changes, or an
   evaluation refreshes a [stale result](#stale-metric-results), the gates that read that
   namespace's MetricChecks and whose expression reads `metrics` are re-evaluated at once.

4. **ChangeWindow change** — When a `ChangeWindow` opens, closes or is edited, the gates whose
   expression reads `changewindow` are re-evaluated at once.

5. **Gate spec or annotation change** — Editing a gate's `spec` or its annotations re-evaluates
   it. So `kubectl annotate policygate <instance> kardinal.io/force-recheck=$(date +%s) --overwrite`
   forces a re-evaluation. The controller's own status writes do not.

6. **PromotionStep created** — When a PromotionStep that has not started is created, the gates in
   its `spec.requiredGates` are re-evaluated at once, so the step can start on a fresh result.

The controller writes the gate's status (`ready`, `reason`, `lastEvaluatedAt`) when the result or
reason changes, when a PromotionStep that has not started waits for a result newer than the stored
one, when the gate's spec changed, and otherwise at least every 10 minutes (the chart's
`controller.gateStatusHeartbeat`, `--gate-status-heartbeat`; `0s` writes on every evaluation). A
re-evaluation that changes nothing writes nothing: every status write makes kro re-check the
Bundle's whole Graph. So `lastEvaluatedAt` can be up to 10 minutes older than the last evaluation.
The Graph reads only
`status.ready`; the PromotionStep also checks that the result is not older than the step before it
starts (see [When a gate holds a step](#when-a-gate-holds-a-step)). While the controller is down,
every gate keeps its last result and no step starts. The controller re-evaluates every gate when it
starts. A result from before an outage still counts for a step created before that result.
The controller evaluates the per-Bundle instances of a gate, never the gate you wrote, so
`kardinal policy list` shows in LAST-EVALUATED the newest evaluation of the gate's instances (with
`--pipeline`, of that pipeline's instances), or `-` when none has been evaluated. An instance
records its gate's name and namespace (the `kardinal.io/gate-template` and
`kardinal.io/gate-template-namespace` labels), so a gate in an org policy namespace or in
`spec.policyNamespaces` counts its instances in every Pipeline's namespace. `kardinal status` shows when each gate holding a Bundle was last checked, and the UI
when each gate of the Bundle on screen was last evaluated, so a stale result is visible.

### ScheduleClock setup

A default `ScheduleClock` is automatically created by the Helm chart in the controller's namespace:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ScheduleClock
metadata:
  name: kardinal-clock
  namespace: kardinal-system
spec:
  interval: 1m   # all time-based gates re-evaluate every minute
```

To verify it is running:

```bash
kubectl get scheduleclock kardinal-clock -n kardinal-system
# NAME             INTERVAL   LAST-TICK                   AGE
# kardinal-clock   1m         2026-04-14T12:00:00Z         5m
```

### Editing a gate

Each Bundle's gate instances are copied from the PolicyGate templates when the controller builds
the Bundle's Graph. Editing a template, for example to fix its expression, does not change the
instances of Bundles already in flight; Bundles created after the edit use the new expression. To
release an in-flight Bundle that the old expression holds, use
[`kardinal override`](#emergency-overrides-k-09) or create a new Bundle.

### Gates of finished Bundles

The gate instances of a finished Bundle keep their last result: they are not evaluated or written
again, and `status.lastEvaluatedAt` stops moving. A Bundle is finished when it is **Superseded**,
or when it is **Verified** and its `GraphReady` condition is `True` (the Graph has seen every
environment verified; nothing reads the gates after that). So the number of gate writes per
ScheduleClock tick follows the number of Bundles in flight, not the Bundle history. The gates of a
**Failed** Bundle are still evaluated, because the Bundle can recover.

## Mid-Flight Policy Changes

PolicyGates are injected into the Graph at Graph creation time (when the Bundle starts promoting). If a new org-level PolicyGate is added while a Bundle is mid-flight, it does not apply to that Bundle's existing Graph. It applies to all subsequent Bundles.

To stop promotions at once, use `kardinal pause <pipeline>`. No new step starts, and in-flight steps hold at the next safe point (steps waiting for a PR merge or running health checks finish). See [Pause and Resume](rollback.md#pause-and-resume).

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

In the UI, the pipeline page's "PolicyGate blocking promotion" banner, its **Show blocked** filter
and the "blocked" count on the Policy Gates panel include only gates that hold the Bundle back, the
same rule as the Blocked label and the Blockers column: the Bundle has reached the gate's
environment (every upstream environment is Verified and the environment has no step yet), or the
environment's `Pending` step requires the gate. A gate that is not ready but does not hold the Bundle,
because its environment is not reached yet or the Bundle failed (it can still retry), is shown as
**Waiting** (grey) instead of **Block** (red). A superseded Bundle's gates that are not ready are
shown as **Superseded**: they are not evaluated again.

`kardinal explain` gives each gate the same state in its STATE column: **Pass** (ready), **Block**
(holding the Bundle), **Superseded**, **Pending** (not evaluated yet) or **Waiting** (evaluated not
ready, not holding the Bundle).

## Approval gates

An approval gate waits for people. Put `spec.approval` on a gate template; the gate is ready only when its expression is true and enough allowed people have approved the Bundle for the environment, and none of them rejected it. It works for `approval: auto` environments as for `pr-review` ones: the environment's PromotionStep is not created until the gate is ready.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: two-approvers
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
spec:
  expression: "true"            # or any expression: both must pass
  message: "prod needs two release managers"
  approval:
    required: 2                 # distinct people (default 1)
    allowedGroups: [release-managers]
    allowedUsers: [oidc:carol@example.com]
    excludeAuthor: true         # the Bundle's verified creator does not count
```

Approve with the CLI:

```bash
kardinal approve my-app-v1-29-0 --env prod --comment "checked the migration"
kardinal approve my-app-v1-29-0 --env prod --decision reject --comment "wait for INC-42"
kardinal approve my-app-v1-29-0 --env prod --revoke
```

```
Recorded: oidc:alice@example.com approves my-app-v1-29-0 for prod (Approval my-app-v1-29-0-prod-3f2a9c1b0d)
```

How it works:

- `kardinal approve` creates an `Approval` object in the Pipeline namespace, labelled with the Bundle and environment, holding the Bundle's UID (`spec.bundleUID`) and owned by the Bundle (deleted with it). It records your Kubernetes username and groups, read from the API server with a SelfSubjectReview. The chart's `<release>-approvals` ValidatingAdmissionPolicy refuses an Approval whose `spec.user` is not the requester, whose `spec.groups` are not among the requester's groups, or whose labels do not match its spec, or that is owned by anything but the Bundle it approves (its owner references cannot change later), and lets only its approver delete it (the garbage collector and the namespace controller excepted; kardinal's controller never deletes Approvals) ([Verified identity](guides/security.md#verified-identity)). An Approval cannot be changed: approving again with another decision replaces yours, and `--revoke` deletes it. The CLI finds your Approval by its labels and `spec.user`, never by name: anyone can create an object under the name it would use, so when that name is taken the API server generates one.
- The promotion Graph reads the Bundle's Approvals with a selector `ref` node and copies those for the environment, for this Bundle's UID and from approvers the gate allows (`allowedUsers`, `allowedGroups`; anyone when both are empty) into each approval gate instance (`spec.approvals`, at most 101; a gate with more than 100 blocks and says so). The allow-lists apply before the cap, so Approvals from people who may not approve cannot crowd out the ones that count. An Approval of an earlier Bundle that had the same name never counts. A new or deleted Approval re-renders the gate at once. Approving before the Bundle reaches the gate is fine.
- The gate counts a decision when its user is in `allowedUsers` or one of its groups is in `allowedGroups`, and, with `excludeAuthor`, the user is not the Bundle's verified creator. Each user counts once. A counted `reject` blocks the gate whatever the approvals. Every decision that appears or is revoked writes an `ApprovalRecorded` or `ApprovalRevoked` AuditEvent.
- With neither `allowedUsers` nor `allowedGroups`, every Approval counts: RBAC on `approvals` decides who can approve, and that includes ServiceAccounts with `create` on `approvals`. Set an allow-list for a gate that only people may pass.
- Group membership is checked when the Approval is created: an approval stays valid after its approver leaves the group. Revoke it by deleting the Approval (its approver) or with a new Bundle. Revoking only matters while the gate waits: once the gate passed and the environment's promotion started, a revoked approval does not stop or undo that promotion (reject the Bundle, or roll back, for that).
- `excludeAuthor` compares approvers with the Bundle's `kardinal.io/created-by` annotation, its verified creator. `kardinal create bundle`, `kardinal promote` and `kardinal rollback` set it to your Kubernetes username, and the chart's `<release>-bundle-creator` policy admits it only in the requester's name and never lets it change. The UI sets the authenticated UI user (`kardinal-ui` without per-user authentication), a Subscription `subscription:<name>`, the Bundle API's static token `bundle-api`, an automatic rollback `kardinal-controller`; only this release's controller ServiceAccount (and the exact usernames in Helm `admission.controllerUsernames`) may name a creator other than itself. A Bundle without the annotation (created with `kubectl` or a GitOps tool without it) blocks a gate with `excludeAuthor`: `excludeAuthor cannot be enforced: the Bundle has no verified creator`. So does a Bundle created by a kardinal component (`subscription:*`, `bundle-api`, `kardinal-ui`, `kardinal-controller`): no person is known to exclude. For CI, create the Bundle as the CI identity: `kardinal create bundle` (or `kubectl create`) with the CI ServiceAccount's kubeconfig records that ServiceAccount, which the policy verifies; with the Bundle API, send the ServiceAccount's own token rather than the shared static token once TokenReview authentication of the Bundle API is enabled (#1511), so the controller records the authenticated caller.
- While it waits, the gate reason is `waiting for approvals: 1 of 2 (alice@example.com)` or `rejected by bob@example.com (wait for INC-42)`; once met, the expression's reason ends with `approved by alice@example.com, bob@example.com (2 of 2)`, which `kardinal explain`, the UI and the PR evidence show. `status.approvals` lists every decision, whether it counts, why not, and when the gate first saw it.
- The expression can read the count: `approvals.count`, `approvals.required`, `approvals.users` and `approvals.rejected` ([CEL context](reference/cel-context.md#approvals)).
- `kardinal override` still force-passes an approval gate, with its own audit record.

Who may approve: bind the chart's `<release>-approvals` ClusterRole (create, delete and read `approvals`, read Bundles, Pipelines and PolicyGates) with a RoleBinding in the Pipeline namespace. The Graph ServiceAccount needs `list` and `watch` on `approvals`, which the chart's `graph-applier` role has.

Trust: anyone allowed to impersonate users or groups (`impersonate` RBAC) can approve as anyone. The identity checks rely on the API server's authentication, so treat `impersonate` as full trust for approvals, overrides and rejections.

Limits:

- An Approval is for one Bundle: the next Bundle needs new approvals.
- In the UI, an approval gate in the Policy Gates panel shows its quorum: one pip per approval it needs, filled for each counted approval, with `1 of 2 approvals`, `Approved (2 of 2)` or `Rejected by bob@example.com`. Below it are who may approve and every decision: who decided, whether it counts and why not, the comment, and when the gate saw it. While the quorum is not met, it shows the `kardinal approve <bundle> --env <env>` command to copy.
- Approving from the UI is not available yet: the UI writes as the controller's ServiceAccount, and the identity policy would refuse an Approval in another name. It comes with per-caller identity in the UI (#1466).
- Upgrading: `helm upgrade` does not update CRDs. Apply `chart/kardinal-promoter/crds/kardinal.io_approvals.yaml` and the updated `kardinal.io_policygates.yaml` before you add `spec.approval` to a gate; a Graph with an approval gate cannot be built without the Approval CRD.

## Emergency Overrides (K-09)

Use `kardinal override` to force-pass a PolicyGate with a mandatory audit record.

```bash
kardinal override my-app --stage prod --gate no-weekend-deploy \
  --reason "P0 hotfix — incident #4521" --expires-in 2h
```

`--gate` takes the name of the PolicyGate you wrote, the template, as `kardinal explain` and `kardinal policy list` show it. The command records a `PolicyGateOverride` entry in `spec.overrides[]` of every instance of that gate that the pipeline's in-progress Bundles have for the stage. With no `--stage`, it records the entry on the instances for every stage. The instances are the per-Bundle copies the Graph creates for each environment, so run the override while the Bundle waits on the gate. Instances of Verified, Failed, Superseded and Rejected Bundles are skipped, because no promotion waits on them; if a Failed Bundle resumes, run the override again. If no in-progress Bundle has an instance, the command fails and says so; it does not write to the template. `--gate` also accepts the name of one instance, as `kubectl get policygates` shows it; the command then records the entry on that instance only. The gate passes immediately until the override ends, and is re-evaluated about a second after that. An override counts from when the controller first saw it, for at most the override cap (`--gate-override-max-minutes`, Helm `controller.gateOverrideMaxMinutes`, 24 hours by default): it ends at the earlier of its `expiresAt` and first-seen time plus the cap. An entry whose `createdAt` is more than 5 minutes after the controller first saw it is not counted (condition `OverrideIgnored`), so a future-dated entry cannot chain overrides past the cap. While the override is active, the instance's `status.reason` is `OVERRIDDEN by <user>: <reason> (expires <end>)`, with the capped end. `--expires-in` defaults to `1h`. kardinal does not remove expired entries from `spec.overrides[]`. They stay on the instance as an audit record, for as long as the Bundle exists. Deleting a Bundle deletes its gate instances and their overrides. That includes the Pipeline's `historyLimit` cleanup: when a new Bundle is created, kardinal deletes the oldest finished Bundles beyond the limit (50 by default). The `GateOverridden` AuditEvent of each override stays after that. Where to see an override:
- `kubectl get policygate <instance> -o yaml` shows every entry in `spec.overrides[]`, active or expired, while the Bundle exists.
- `kardinal explain` shows the `OVERRIDDEN by ...` reason in the gate's REASON column while the override is active.
- The PR evidence body has no separate badge. A PR opened while the override is active lists the gate in its Policy Gate Compliance table with Result `Pass` and the `OVERRIDDEN by ...` reason. The body is written when the PR is opened and is not updated afterwards, so an override recorded after that, or one that expired before it, does not appear there. An `auto` environment opens no PR.
- `kardinal get auditevents` lists a `GateOverridden` AuditEvent for each override: the controller writes one the first time it sees an entry in `spec.overrides[]`, stamped with that time, with who created it (and whether that was verified), the stage, the capped end and the reason. The instance's `status.overrides` keeps one record per entry (`key`, `firstSeen`, `verified`, `audited`) so it is written once; records are never dropped, so an entry removed and added again keeps its first-seen time. Past 200 records a new override is not counted (`OverrideIgnored`).

**Who overrode the gate is verified.** `createdBy` is your Kubernetes username, which `kardinal override` reads from the API server with a SelfSubjectReview (what `kubectl auth whoami` shows), not your local OS user. The chart's `<release>-gate-overrides` ValidatingAdmissionPolicy refuses a new or changed `spec.overrides[]` entry whose `createdBy` is not the requesting user, so `kubectl edit` cannot record an override in someone else's name either. The one exception is the controller's own ServiceAccount, which writes overrides for the UI: with `ui.auth.tokenReview` the UI records the username the API server authenticated, otherwise `kardinal-ui` ([UI API Access Control](guides/security.md#ui-api-access-control)). The same policy lets only kro (the namespace's Graph ServiceAccount) and the controller create a gate instance (a PolicyGate with the `kardinal.io/bundle` label) or change anything in it but `spec.overrides`, its whole metadata included (labels, annotations but `kardinal.io/force-recheck`, owner references, finalizers): an override is the audited way to pass a gate. An override is recorded as verified only when the controller first sees it while that policy and its binding exist; one already on a gate when you upgraded to a release with these checks, or seen while the policy was missing, is shown and audited as unverified (`OVERRIDDEN by <user> (unverified)`), since nothing checked its `createdBy`. Anyone who may update PolicyGates can remove an override or re-add it in their own name; the record and its AuditEvent stay. Revoking an override after the gate passed does not undo a promotion that already went through. See [Verified identity](guides/security.md#verified-identity).

### Rollback hold exemption

A rollback created with `kardinal rollback --hold` can pass gates that would block it, for as
long as the hold lasts. This is exactly what is exempt:

- **Exempt:** the gate instances of the held environment (`kardinal.io/environment` equals the
  hold's environment) for the Bundle the hold names.
- **Not exempt:**
    - gates of every other environment, including the ones the rollback crosses before the
      held one;
    - the freeze gate of a paused Pipeline, which holds steps on its own;
    - approval gates (an `approval` policy, or an expression on `approvals.*`), met or not: a
      hold never stands in for the people a quorum requires;
    - any Bundle but the one the hold names.

The exemption applies only to a rollback the controller verifies, at every evaluation:

1. The Bundle is a rollback Bundle (`kardinal.io/rollback=true`) of the Pipeline.
2. Its `spec.provenance.rollbackOf` names a Bundle that was Verified in the held environment.
3. The combination of artifacts is checked per repository. For every image repository, config
   repository and chart that `rollbackOf` deploys, the rollback deploys exactly `rollbackOf`'s
   ref. Only repositories `rollbackOf` does not name (the ones `kardinal rollback` fills from
   earlier Bundles) may come from another Bundle Verified in that environment. The rollback
   therefore never mixes a version of one repository with another version of the same repository
   than the target ran.
4. Its artifacts still have the digest the hold recorded when it was made
   (`spec.holds[].artifacts`: SHA-256 of the JSON of its type, images, configRef and chart). A
   Bundle edited after the hold is not exempt.
5. The hold has not expired. A hold past its `expiresAt` counts as absent at once, also before
   the controller removes it from the spec.

When a hold names the Bundle but the checks fail, the gate blocks as usual, and its reason ends
with `(hold exemption refused: <why>)`.

An exempt gate has the `status.reason`
`EXEMPT: rollback <bundle> holds <env> (by <user>: <reason>); without the hold: <the gate's own result>`.
The flip is a `GateEvaluated` AuditEvent, and the gate gets a `GateExempted` Warning Event, so
`kardinal explain`, `kardinal audit` and `kubectl get events` all show it.
`kardinal release-hold` ends the exemption, and the gates are evaluated again at once. See
[Roll back and hold](rollback.md#roll-back-and-hold).
