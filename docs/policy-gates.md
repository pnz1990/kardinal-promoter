# Policy Gates

A gate holds a version before an environment until its rule passes: a time window, a soak in the
environment before, a metric, an approval. The rule is a CEL expression, and
[`kardinal why`](reference/cli/kardinal-why.md) shows what a gate waits for.

## Add a gate

This gate holds prod until the version has been live in uat for 5 minutes. Create it in the
Pipeline's namespace (`default` for the [quickstart](quickstart.md)'s `demo`):

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: uat-soak
  namespace: default
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "upstream.uat.soakMinutes >= 5"
  message: "prod waits until the version has been live in uat for 5 minutes"
  recheckInterval: 1m
EOF
```

`kardinal.io/applies-to` names the environment it holds. Ship a version: once uat is live,
`kardinal why demo prod` shows the gate while it holds, for example:

```text
prod is held by gate uat-soak.
  Version  main (demo-q8m4t), by kubernetes-admin
  Why      prod waits until the version has been live in uat for 5 minutes
  Rule     upstream.uat.soakMinutes >= 5
  Now      soak 1.2 of 5 min in uat
  Opens    by itself in about 3m48s; rechecked every 1m
  Next     wait: it opens by itself in about 3m48s (kardinal status demo -w follows it)
  Or       kardinal override demo prod --gate uat-soak --reason <why>
```

After 5 minutes the gate passes and prod promotes (in the quickstart's `demo`, prod then still waits for your approval at `prod-approval`).
[CEL Context](#cel-context) lists what an expression can read, and [CEL Expression Examples](#cel-expression-examples) has more rules.

## When a new or edited gate takes effect

!!! info "Changed in v0.13.0 (unreleased)"
    In v0.12.0 and earlier, a version in flight keeps the gates it started with: a new or edited
    gate applies only to versions created after it, and gate copies carry no
    `kardinal.io/gate-template-*` annotations and write no `GateChanged` record. The
    [changelog](changelog.md) lists every change since v0.12.0.

A version gets its gates when it starts promoting: kardinal copies every gate that applies to its
environments into the version then. When you create, edit or delete a gate later, kardinal copies
the gates again into every version that is still promoting, so:

- **A new gate** holds the environments of those versions that have not started yet.
- **An edited gate** (its expression, message, recheck interval or approval policy) is evaluated
  with the new rule at once: a gate that passed can block again, and one that blocked can pass.
- **A deleted gate** no longer holds them.

An environment that has already started is not affected: a gate only decides whether an
environment may start. A version that is verified, superseded or rejected keeps the gates it had,
and so does a failed one until it recovers and promotes again. An [override](overrides.md) on a
version's gate stays in force: kardinal never copies a gate's overrides.

If a version cannot be built with the changed gates (for example, a skip-permission gate that was
given an approval policy), it keeps the gates it has and goes on promoting. Its `GraphSynced`
condition is `False` with the reason, and kardinal tries again every 30 seconds and at each change
to the gates.

Each copy of a gate records the gate it came from in two annotations:
`kardinal.io/gate-template-generation` is the gate's `metadata.generation`, and
`kardinal.io/gate-template-digest` is `sha256:` and the SHA-256 of the gate's `spec` without
`spec.overrides`. The changes that reach a promoting version are recorded as one `GateChanged` AuditEvent per
rebuild, listing each template with its old and new digest, and the environments it moved to or from when its `kardinal.io/applies-to` label changed ([Events written automatically](guides/security.md#events-written-automatically)):
`kardinal get auditevents --bundle <bundle>` lists them. To see which revision of each gate holds a version:

```bash
kubectl get policygate -n <pipeline-namespace> -l kardinal.io/bundle=<bundle> \
  -o custom-columns='GATE:.metadata.labels.kardinal\.io/gate-name,ENV:.metadata.labels.kardinal\.io/environment,GENERATION:.metadata.annotations.kardinal\.io/gate-template-generation'
```

To act on a version in flight at once: `kardinal pause <pipeline>` stops new steps (see
[Pause and Resume](pause.md)), `kardinal hold <pipeline> <env>` keeps an environment on what it
runs, and [`kardinal override`](overrides.md) releases a version that a gate holds.

## How gates work

1. A platform team defines PolicyGate CRDs in the `platform-policies` namespace (org-level) or in a team namespace (team-level).
2. When a version is created and a Graph is generated from the Pipeline, the controller collects all matching gates and injects them as nodes in the Graph between the upstream environment and the target environment.
3. The Graph controller creates per-version gate instances.
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
    kardinal.io/scope: <string>         # optional; "org" makes a gate outside the org policy namespaces an org gate
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
Only the gates kardinal creates can have longer names: the per-version gate instances and the
`freeze-<pipeline>` gate of a paused Pipeline. Kardinal sets `spec.generated: true` on them. Do not
set it on a gate you write: kardinal never uses a gate with `spec.generated` as a template, so the
gate would not apply to any environment.

### What users see when a gate blocks

When a gate's expression is false, its `status.reason` starts with `spec.message` and ends with the
evaluated result and the version in parentheses:

```
Production deployments are blocked on weekends (!schedule.isWeekend = false; version 1.29.0)
```

The gate's `Ready` condition, `kardinal why -o wide` and the UI show this reason. `kardinal status
<pipeline>` shows the message alone under Blocking gates. The Pending step's message names
the gate instance and the gate's message: `waiting for gate
no-weekend-deploys-platform-policies-prod--<bundle>: Production deployments are blocked on
weekends`. An instance name that is too long, or has characters a name cannot hold, ends in a hash.
A gate without a message shows the result, then the version (`!schedule.isWeekend = false (version
1.29.0)`), and an approval gate the count (`waiting for approvals: 0 of 1 (version 1.29.0)`), also
when it has a message and its expression passes. The
message is not shown when the gate blocks for another reason: an evaluation error (`CEL evaluation
error: ... (version 1.29.0)`) or a context error (`context error: ...`) keeps its own reason,
because the message does not explain it. The version is also in `status.bundleVersion`, for
scripts.

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

While a gate that is not ready holds a step, `kardinal status` says "Held by gate <name>" for that
environment and lists the gate under Blocking gates, and the UI counts it as a blocker (the Blocked label and the Blockers column). A ready gate whose
result is older than the step still shows as ready while the step waits for its re-evaluation; the
step's message says which gate it waits for.

Limits:

- **A step that has started is not stopped.** Once the step leaves `Pending`, a gate that turns
  false does not interrupt it: gates are not evaluated against a deployment in progress. Use `bake`
  for post-deployment soak and `health` for health checks. While a `pr-review` step's PR waits for
  its merge, the gates keep being evaluated and are shown on the PR as the `kardinal/gates` commit
  status; with branch protection requiring that check, a gate that turns false blocks the merge
  ([Gate status check](pr-evidence.md#gate-status-check-kardinalgates)).
- **Steps wait if gates cannot be evaluated.** A step that needs a fresh result waits until the
  controller writes one (it fails closed).
- **Clock skew.** The API server sets the step's `creationTimestamp`; the controller's clock sets
  the gate's `lastEvaluatedAt`. Both have one-second precision, so a result from the same second as
  the step counts. If the controller's clock is behind the API server's, a fresh result can look
  older than the step, and the step waits for a later evaluation (the next ScheduleClock tick or
  `recheckInterval` after the skew has passed). If the controller's clock is ahead, a result up to
  the skew old is accepted. Keep node clocks synchronized. This effect is not covered by tests.

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

Created in the `platform-policies` namespace (configurable via `--policy-namespaces` controller flag). Org gates are mandatory: they are injected into every Pipeline that targets the matching environment. Teams cannot remove them. A gate in an org policy namespace is an org gate without the `kardinal.io/scope` label: `kardinal policy list` and its instances show `SCOPE org` (from v0.13.0, unreleased; v0.12.0 shows such a gate as `team` while it acts as an org gate). The label `kardinal.io/scope: org` makes a gate in another namespace an org gate too. `kardinal policy list` reads the org policy namespaces from its own `--policy-namespaces` flag: pass the controller's value when it is not the default.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend-deploys
  namespace: platform-policies
  labels:
    kardinal.io/applies-to: prod
    kardinal.io/type: gate
spec:
  expression: "!schedule.isWeekend"
  message: "Production deployments are blocked on weekends"
  recheckInterval: 5m
```

### Team-level gates

Created in the team's own namespace. Team gates are additive: they are injected alongside org gates. A team Pipeline can add restrictions but cannot remove or weaken org gates. The escape hatch is [`kardinal override`](overrides.md): it patches the gate instances in the Pipeline namespace, so anyone with `patch` on PolicyGates there can force-pass an org gate for a limited time, with the reason recorded. Grant that verb only to the people allowed to break glass.

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
`prod-us,prod-eu`. To block several environments, create one gate per environment,
each with its own `kardinal.io/applies-to` value.

The controller reads gates from these places:
1. The `--policy-namespaces` flag namespaces (default: `platform-policies`). These are the org policy namespaces.
2. The Pipeline's own namespace.
3. Any namespaces listed in the Pipeline's `spec.policyNamespaces`.

Every matching gate from all three is added to the Graph. `spec.policyNamespaces` can only add namespaces; the org policy namespaces are read whatever it says. A gate from the Pipeline's namespace or from `spec.policyNamespaces` is a team gate, unless it is labelled `kardinal.io/scope: org`. Either way, it can never grant a skip (see [Skip Permissions](#skip-permissions)).

### Sharing gates with other namespaces

!!! info "New in v0.13.0 (unreleased)"
    v0.12.x and earlier read every namespace `spec.policyNamespaces` names, without its consent
    and without a condition or Event.

A gate read through `spec.policyNamespaces` is copied into the Pipeline's namespace, where
everyone who may read that namespace sees its expression, name and labels. So a namespace other
than the org policy namespaces must agree to be named there, in one of two ways:

- its Namespace carries the label `kardinal.io/policy-shareable: "true"`
  (`kubectl label namespace shared-gates kardinal.io/policy-shareable=true`);
- it is listed in the controller's `--policy-shareable-namespaces` (Helm
  `controller.policyShareableNamespaces`).

The label agrees for every Pipeline of the cluster, not one team's. It is exactly as strong as
the right to edit that Namespace's labels: `update` or `patch` on `namespaces`, granted cluster-wide
or in that namespace. Kubernetes' stock `admin` and `edit` roles do not grant it, but platforms that
let tenants edit their Namespace's labels (a self-service ClusterRole, Capsule, the Hierarchical
Namespace Controller) hand the consent to whoever has that right. On such a cluster, either list the
namespaces in `controller.policyShareableNamespaces` only, or limit who may set the label with a
ValidatingAdmissionPolicy:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: policy-shareable-label
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["namespaces"]
  variables:
    - name: label
      expression: '"kardinal.io/policy-shareable"'
    - name: before
      expression: 'oldObject != null && has(oldObject.metadata.labels) && variables.label in oldObject.metadata.labels ? oldObject.metadata.labels[variables.label] : ""'
    - name: after
      expression: 'has(object.metadata.labels) && variables.label in object.metadata.labels ? object.metadata.labels[variables.label] : ""'
  validations:
    - expression: 'variables.before == variables.after || "platform-admins" in request.userInfo.groups'
      message: only platform-admins may set kardinal.io/policy-shareable
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: policy-shareable-label
spec:
  policyName: policy-shareable-label
  validationActions: ["Deny"]
```

A Pipeline whose `spec.policyNamespaces` names a namespace that has not agreed gets the condition
`PolicyNamespacesShared=False` with reason `NotShared`, naming each such namespace, and one Warning
Event `PolicyNamespaceNotShared` each time that list changes. A namespace that does not exist is
named like one that is not labelled; the controller log tells them apart. In this release the gates
of those namespaces are still read and `Ready` is unchanged; a later release refuses them (#2286).

The controller reads the named Namespaces again every 5 minutes, so a label added or removed later
shows within that time. When a read fails (an API error other than not found), the namespace keeps
the result of its last read: the condition's reason becomes `PolicyNamespaceUnreadable`, its list
stays as it was, and the read is retried within a minute. An API blip neither raises nor clears the
warning.

To list the Pipelines that name a namespace that has not agreed (every one a later release refuses),
run `kardinal doctor`. Its "Policy namespaces shared" check names each such Pipeline and the
namespaces, and reports a WARN against this release and a FAIL against v0.14, which refuses them.
Against a controller older than v0.13, which doesn't check, it warns that it can't tell. Without
the CLI:

```bash
kubectl get pipelines -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.status.conditions[?(@.type=="PolicyNamespacesShared")].status}{"\n"}{end}' | awk -F'\t' '$2=="False"{print $1}'
```

The Graph creates every gate instance in the Pipeline namespace and labels it
`kardinal.io/gate-template-namespace` with its template's namespace. A gate from an org policy
namespace reads `metrics.*` from the MetricChecks of that namespace, so the platform team owns
the MetricChecks its gates use and a team MetricCheck of the same name cannot decide them. Every
other gate reads the MetricChecks of the Pipeline namespace.

A team can edit the labels of the instances in its namespace, so the controller trusts the label
only when the namespace it names is an org policy namespace and holds the gate that the
instance's `kardinal.io/gate-template` label names. Otherwise the instance reads the Pipeline
namespace, where a metric it names but cannot find still blocks it. If the controller cannot read
the template, the gate evaluates with no metrics, so an expression that reads `metrics.*` blocks.

## CEL Context

All gate expressions are evaluated against the same context. The
[CEL context reference](reference/cel-context.md) lists every attribute with its type; a test
evaluates every attribute and example on these pages against the controller's real context.
Referencing an attribute or map key that does not exist is an evaluation error, and the gate
blocks (fail-closed).

The context has the version (`bundle.*`, with `bundle.upstreamSoakMinutes` and the stage PRs'
review state in `bundle.pr`), who created it (`actor.*`) and its agent's registration
(`agent.*`), the time (`schedule.*`), the target environment (`environment.name`),
per-environment soak and history (`upstream.*`), MetricCheck results (`metrics.*`), change
windows (`changewindow`), change request tickets (`changeRequests.*`, see
[Change Requests](change-requests.md)) and approvals (`approvals.*`).

### Actor attributes

`actor.*` is the version's verified actor stamp ([Actors](actors.md)): human, agent,
bot or controller, and for an agent its name and sponsor. A version without a stamp reads as
kind `unknown`. `agent.*` is the version's agent as the registry says now, so a suspension holds
its in-flight versions. The [CEL context](reference/cel-context.md#actor) lists every field.

| Attribute | Type | Description |
|---|---|---|
| `actor.kind` | string | `human`, `agent`, `bot`, `controller`, or `unknown` without a stamp |
| `actor.verified` | bool | The version has a stamp |
| `actor.agent` | string | The agent's name, when the kind is `agent` |
| `actor.via` | string | `ui` or `api` when the controller wrote the version for its caller, `subscription` or `auto-rollback` for its own; else `""` |
| `agent.registered` | bool | The agent's AgentIdentity is ready |
| `agent.autonomy` | string | `auto`, `approve` or `deny`, now |

```yaml
# Require a verified actor: refuse versions from v0.11 clients and hand-written Bundles
expression: actor.verified
---
# People only in prod
expression: actor.verified && actor.kind == "human"
---
# Hold an agent's version once its agent is suspended or deregistered
expression: actor.kind != "agent" || (agent.registered && !agent.suspended)
---
# Let the controller's automatic rollback through
expression: actor.kind == "controller" && actor.via == "auto-rollback"
```

!!! warning "Never trust a version's labels or annotations for who made it"
    Whoever creates a Bundle writes its labels and annotations, `kardinal.io/rollback` and
    `kardinal.io/rollback-from` included, and no admission rule checks what they say when the
    Bundle is created. (From v0.13.0, unreleased: on a Bundle the controller created, only the controller may change those
    two and `kardinal.io/pipeline` afterwards: [Rollback](rollback.md#what-happens-in-git).) A gate
    such as `"kardinal.io/rollback" in bundle.labels` lets through any version that says it is a
    rollback.
    Check the actor stamp instead, which the chart's admission lets only the controller write for
    its own versions: `actor.kind == "controller" && actor.via == "auto-rollback"` for an automatic
    rollback.

For per-environment autonomy, set `agents` on the Pipeline's environment instead of writing
gates ([Actor-aware policy](agentic/policy.md)).

### Metric attributes

`metrics.<name>` reads the MetricCheck named `<name>` in the gate's [metrics namespace](#matching)
(the org policy namespace for an org gate, the Pipeline namespace otherwise).
MetricChecks query Prometheus, Datadog, CloudWatch, New Relic or any JSON web API, with credentials from Secrets; a `perPromotion` MetricCheck is instantiated for each version and environment with its `bundle.version` (the image tag, or the config commit of a config version) in its query, and the gate of that version reads its own instance as `metrics.<name>` (see [Metric Checks](metric-checks.md)).

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
The controller evaluates every MetricCheck when it starts.

### Cross-stage history attributes

Available for all gates. The history is computed from Bundle CRD status across the last 10
promotions for the pipeline — no external API calls. The lookup is scoped to the last 10
versions by creation time. `upstream` has an entry for every environment in this version's
status or in that history (not only the gated environment's upstreams); any other name is an
evaluation error and the gate blocks.

### PR review attributes

Available when the stage has `approval: pr-review`. The review state is written by the
PRStatus controller to `PRStatus.status.approved` / `status.approvalCount` — no
external API calls are made during gate evaluation.

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

### ChangeWindow attributes

A `ChangeWindow` is a cluster-scoped object that describes when promotions are blocked.
It blocks nothing by itself: a gate blocks while a window it references is active.
Reference windows from an org-level gate to freeze every pipeline with one object. A
ChangeWindow with `spec.freeze` (`kardinal freeze`) is the exception: it holds the promotions it
selects without any gate, agents' versions or everyone's ([Freeze agents, stop the
line](agentic/freezes.md)).

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

# Equivalent map syntax
expression: '!changewindow["q4-holiday-freeze"]'

# Only promote inside business hours, and never during the freeze
expression: 'changewindow.isAllowed("business-hours") && !changewindow.isBlocked("q4-holiday-freeze")'
```

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

### Version-based

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

While a soak gate (`bundle.upstreamSoakMinutes` or `upstream.<env>.soakMinutes` compared with
`>=` or `>`) holds a version, `kardinal why` (also with `-o wide`) and `kardinal status` show how far
it soaked and, when the comparison is the whole expression, when it opens:

```
$ kardinal why demo prod
prod is held by gate uat-soak-before-prod.
  Version  sha-9349a3f (demo-kzwhs), by kubernetes-admin
  Rule     bundle.upstreamSoakMinutes >= 3
  Now      soak 0.4 of 3 min in uat
  Opens    by itself in about 2m36s; rechecked every 30s
  Next     wait: it opens by itself in about 2m36s (kardinal status demo -w follows it)
  Or       kardinal override demo prod --gate uat-soak-before-prod --reason <why>
```

The soak is counted from the upstream's `status.environments[].healthCheckedAt` on the Bundle,
the same field the controller's `soakMinutes` counts from; the gate itself decides on whole minutes.

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

A version can skip environments with `intent.skipEnvironments`. When an org-level gate applies to a skipped environment, the skip needs a skip-permission gate. The permission counts only if it meets all of these conditions:

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

The controller checks for the permission when it builds the Bundle's Graph. If no permission exists, the version goes to phase `Failed`, and its status conditions give the reason: `skip denied for environment "staging": ...`. The version does not promote.

If a permission exists, the controller evaluates its expression like any other gate. It creates an instance of the permission gate in front of the next environment the version promotes. That environment, `prod` in a `test → staging → prod` pipeline, waits until the expression is true. It never starts for a version the permission does not cover. `kardinal why -o wide` shows the instance with the other gates of that environment. The instance is annotated `kardinal.io/skipped-environments: staging`. If the skipped environment is the last one, nothing comes after it, so nothing waits on the permission.

Without a skip-permission gate, skipping an environment that has org gates is always denied. An environment that only team gates apply to can be skipped without a permission.

## Re-evaluation

Gates are re-evaluated when any of the following occurs:

1. **ScheduleClock tick** (primary mechanism) — A `ScheduleClock` object in `kardinal-system`
   writes `status.tick` every minute by default. The PolicyGate reconciler watches all `ScheduleClock`
   objects; each tick triggers re-evaluation of all active gate instances cluster-wide
   (not those of [finished versions](#gates-of-finished-versions)). Creating, editing or deleting a clock does not.
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
version's whole Graph. So `lastEvaluatedAt` can be up to 10 minutes older than the last evaluation.
The Graph reads only
`status.ready`; the PromotionStep also checks that the result is not older than the step before it
starts (see [When a gate holds a step](#when-a-gate-holds-a-step)). While the controller is down,
every gate keeps its last result and no step starts. The controller re-evaluates every gate when it
starts. A result from before an outage still counts for a step created before that result.
The controller evaluates the per-version instances of a gate, never the gate you wrote, so
`kardinal policy list` shows in LAST-EVALUATED the newest evaluation of the gate's instances (with
`--pipeline`, of that pipeline's instances), or `-` when none has been evaluated. An instance
records its gate's name and namespace (the `kardinal.io/gate-template` and
`kardinal.io/gate-template-namespace` labels), so a gate in an org policy namespace or in
`spec.policyNamespaces` counts its instances in every Pipeline's namespace. `kardinal status` shows when each gate holding a version was last checked, and the UI
when each gate of the version on screen was last evaluated, so a stale result is visible.

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

### Gates of finished versions

The gate instances of a finished version keep their last result: they are not evaluated or written
again, and `status.lastEvaluatedAt` stops moving. A version is finished when it is **Superseded** or
**Rejected**, or when it is **Verified** and its `GraphReady` condition is `True` (the Graph has seen every
environment verified; nothing reads the gates after that). So the number of gate writes per
ScheduleClock tick follows the number of versions in flight, not the version history. The gates of a
**Failed** version are still evaluated, because the version can recover.

## Inspecting gates

```bash
# List all PolicyGates
kardinal policy list

# See which gates are blocking a promotion
kardinal why my-app prod -o wide

# Validate a PolicyGate file
kardinal policy test my-gate.yaml

# Simulate a gate against hypothetical conditions
kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"
```

In the UI, the pipeline page's "PolicyGate blocking promotion" banner, its **Show blocked** filter
and the "blocked" count on the Policy Gates panel include only gates that hold the version back, the
same rule as the Blocked label and the Blockers column: the version has reached the gate's
environment (every upstream environment is Verified and the environment has no step yet), or the
environment's `Pending` step requires the gate. A gate that is not ready but does not hold the version,
because its environment is not reached yet or the version failed (it can still retry), is shown as
**Waiting** (grey) instead of **Block** (red). A superseded version's gates that are not ready are
shown as **Superseded**, and a rejected version's (`kardinal reject`) as **Rejected**: they are not evaluated again.

`kardinal why -o wide` gives each gate the same state in its STATE column: **Pass** (ready), **Block**
(holding the version), **Superseded**, **Rejected**, **Pending** (not evaluated yet) or **Waiting** (evaluated not
ready, not holding the version).

## Rollback hold exemption

A rollback created with `kardinal rollback --hold` can pass gates that would block it, for as
long as the hold lasts. This is exactly what is exempt:

- **Exempt:** the gate instances of the held environment (`kardinal.io/environment` equals the
  hold's environment) for the version the hold names.
- **Not exempt:**
    - gates of every other environment, including the ones the rollback crosses before the
      held one;
    - the freeze gate of a paused Pipeline, which holds steps on its own;
    - approval gates (an `approval` policy, or an expression on `approvals.*`), met or not: a
      hold never stands in for the people a quorum requires;
    - any version but the one the hold names.

The exemption applies only to a rollback the controller verifies, at every evaluation:

1. The version is a rollback version (`kardinal.io/rollback=true`) of the Pipeline.
2. Its `spec.provenance.rollbackOf` names a version that was Verified in the held environment.
3. The combination of artifacts is checked per repository. For every image repository, config
   repository and chart that `rollbackOf` deploys, the rollback deploys exactly `rollbackOf`'s
   ref. Only repositories `rollbackOf` does not name (the ones `kardinal rollback` fills from
   earlier versions) may come from another version Verified in that environment. The rollback
   therefore never mixes a version of one repository with another version of the same repository
   than the target ran.
4. Its artifacts still have the digest the hold recorded when it was made
   (`spec.holds[].artifacts`: SHA-256 of the JSON of its type, images, configRef and chart). A
   version edited after the hold is not exempt.
5. The hold has not expired. A hold past its `expiresAt` counts as absent at once, also before
   the controller removes it from the spec.

When a hold names the version but the checks fail, the gate blocks as usual, and its reason ends
with `(hold exemption refused: <why>)`.

An exempt gate has the `status.reason`
`EXEMPT: rollback <bundle> holds <env> (by <user>: <reason>); without the hold: <the gate's own result>`.
The flip is a `GateEvaluated` AuditEvent, and the gate gets a `GateExempted` Warning Event, so
`kardinal why -o wide`, `kardinal audit` and `kubectl get events` all show it (the Event is best
effort; the AuditEvent and `status.reason` are the durable record: [Kubernetes Events](operate.md#kubernetes-events)).
`kardinal unhold` ends the exemption, and the gates are evaluated again at once. See
[Roll back and hold](rollback.md#roll-back-and-hold).
