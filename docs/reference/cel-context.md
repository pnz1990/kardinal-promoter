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
| `changewindow` | map | ChangeWindow name → `true` while the window is active (blocking); also `changewindow.isBlocked("name")` and `changewindow.isAllowed("name")`. An unknown name blocks the gate. See [ChangeWindow attributes](../policy-gates.md#changewindow-attributes) |
| `changeRequests` | map | The change request tickets of this gate instance's promotion (ServiceNow, Jira). See [`changeRequests.*`](#changerequests) |
| `approvals` | map | The Approvals counted for this gate instance (`kardinal approve`). See [`approvals.*`](#approvals) |
| `verdicts` | map | The Verdicts a verify gate counted (`kardinal verify`, AgentRuns). See [`verdicts.*`](#verdicts) |
| `actor` | map | Who created the version: its verified actor stamp. See [`actor.*`](#actor) |
| `agent` | map | The version's agent as the registry says now. See [`agent.*`](#agent) |
| `attestations` | map | The version's verified attestations: SLSA provenance, vulnerability scans after VEX, any in-toto predicate. See [`attestations.*`](#attestations) |
| `commit` | map | The config commit's verified signature, and the agent whose workload signed it. See [`commit.*`](#commit) |

---

## `bundle.*`

| Field | Type | Example | When populated |
|---|---|---|---|
| `bundle.type` | string | `"image"` | Always |
| `bundle.createdBy` | string | `"oidc:alice@example.com"` | The Bundle's verified creator (`kardinal.io/created-by`); `""` when it has none. Kept as an alias of `actor.principal` for one release |
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

The decisions `kardinal approve` recorded for the gate instance's Bundle and environment (the Graph copies them into `spec.approvals`). With `spec.approval` set, only allowed approvers count, and with `excludeAuthor` nobody of the author's party does (see [Approval gates](../approvals.md)); without it, every person's approval counts.

| Field | Type | Example | When populated |
|---|---|---|---|
| `approvals.count` | int | `2` | Always; `0` with no approval |
| `approvals.required` | int | `2` | `spec.approval.required`; `0` without `spec.approval` |
| `approvals.users` | list | `["alice@example.com"]` | Always; the users whose approval counts, sorted |
| `approvals.rejected` | bool | `false` | Always; `true` when a counted user rejected |
| `approvals.met` | bool | `false` | Always; `true` when the gate has no policy, its policy is met, or `approval.requiredWhen` is false now |
| `approvals.fields.<name>` | list of strings | `["CHG0031337"]` | (new in v0.13.0) For each field the gate declares in `approval.fields`: the values the counted approvals gave, in the order of `approvals.users` (empty when none did). A name the gate does not declare is an evaluation error, so the gate fails closed ([Approval fields](../approvals.md#approval-fields)) |

Decisions count per party: a person, or an agent together with its sponsor, is one vote, and
`approvals.count` and `approvals.users` count and list parties. An agent's approval counts only on
a gate with `approval.allowAgents` that lists the agent ([Agents as approvers](../approvals.md#agents-as-approvers)).

```yaml
# Two approvals, outside the weekend
expression: 'approvals.count >= 2 && !approvals.rejected && !schedule.isWeekend'
```

## `changeRequests.*`

New in v0.13.0: v0.12.0 has no `changeRequests`.

One entry per per-promotion `ChangeRequest` template (`spec.perPromotion: true`) in the gate's
metrics namespace, keyed by the template's name. The value is the instance the Graph made from it
for the gate's own Bundle and environment (labels `kardinal.io/change-request-template`,
`kardinal.io/bundle`, `kardinal.io/environment`). The ChangeRequest controller reads the ticket and
writes all of it to the instance's status; the gate never calls the ticketing system. See
[Change requests](../change-requests.md).

| Field | Type | Example | When populated |
|---|---|---|---|
| `changeRequests["prod-change"].state` | string | `"approved"` | `pending`, `approved`, `rejected`, `cancelled`, `closed` or `unknown`; `""` until the instance has a ticket; `"unknown"` once the reading is stale |
| `changeRequests["prod-change"].ticket` | string | `"CHG0031337"` | The ServiceNow number or Jira issue key; `""` until there is one |
| `changeRequests["prod-change"].url` | string | `"https://example.service-now.com/..."` | The ticket's page |
| `changeRequests["prod-change"].stale` | bool | `false` | `true` before the first reading, and when the gate's evaluation time is at or after the instance's `status.validUntil` (two poll intervals after its last reading) |
| `changeRequests["prod-change"].timedOut` | bool | `false` | `true` once `spec.timeout` has passed since the instance started |
| `changeRequests["prod-change"].verified` | bool | `true` | `true` for a ticket kardinal opened, and for a linked ticket that carries the promotion's correlation id, or that matches `link.requireField`, was created after the Bundle and is linked to no other promotion; `false` otherwise |
| `changeRequests["prod-change"].verifiedReason` | string | `"the ticket was created ..., before Bundle ..."` | Why a linked ticket is or is not verified; `""` for a ticket kardinal opened |
| `changeRequests["prod-change"].approvers` | list | `["jane@example.com"]` | The ticket's approvers, when the provider reports them |
| `changeRequests["prod-change"].providerState` | string | `"Scheduled / approved"` | The ticketing system's own state, for people |

A name with no template is an evaluation error, so the gate fails closed. An org gate (a template in
an org policy namespace) reads that namespace, where the Graph creates no instances, so its
`changeRequests` entries never have a ticket.

```cel
# Prod needs an approved, fresh change request
changeRequests["prod-change"].state == "approved" && !changeRequests["prod-change"].stale

# A linked ticket must be verified, and nobody may wait more than the timeout
changeRequests["prod-change"].state == "approved" && changeRequests["prod-change"].verified && !changeRequests["prod-change"].timedOut
```

## `verdicts.*`

The votes a verify gate (`spec.verify`) counted from the Verdicts of the gate instance's Bundle for its check and verified environment (the Graph copies them into `spec.verdicts`), by the rules of [Verify with agents](../agentic/verify-with-agents.md#how-verdicts-count). A gate without `spec.verify` gets zeros.

| Field | Type | Example | When populated |
|---|---|---|---|
| `verdicts.passed` | int | `2` | Always; counted pass votes |
| `verdicts.failed` | int | `0` | Always; counted fail votes (and inconclusive ones with `onInconclusive: fail`) |
| `verdicts.inconclusive` | int | `1` | Always; inconclusive votes that wait |
| `verdicts.required` | int | `2` | `spec.verify.required`; `0` without `spec.verify` |
| `verdicts.verifiers` | list | `["qa-explorer", "qa-explorer"]` | Always; the passing voters (agent or principal), oldest first |
| `verdicts.timedOut` | bool | `false` | Always; `true` past `spec.verify.timeout` |
| `verdicts.findings.high` | int | `0` | Always; high or critical findings in counted votes |

```yaml
# The quorum, and no high finding among the counted verdicts
expression: 'verdicts.findings.high == 0'
```

---

## `actor.*`

Who created the version: the Bundle's verified actor stamp (`spec.actor`,
[Actors](../actors.md)). It is fixed when the version is created. A version
without a stamp (a v0.11 client, or kubectl) reads as kind `unknown`, with its
`kardinal.io/created-by` as the principal. What a client asserts about itself (model, session,
client) is not in the context: it is text the client writes, and no such text reaches a gate.

| Field | Type | Example | When populated |
|---|---|---|---|
| `actor.kind` | string | `"agent"` | Always: `human`, `agent`, `bot`, `controller`, or `unknown` without a stamp |
| `actor.principal` | string | `"system:serviceaccount:ci:fixer"` | The authenticated username; without a stamp `kardinal.io/created-by`; else `""` |
| `actor.agent` | string | `"fixer"` | The AgentIdentity name when the kind is `agent`; else `""` |
| `actor.sponsor` | string | `"alice@example.com"` | The agent's sponsor when the version was created, when the kind is `agent`; else `""` |
| `actor.via` | string | `"ui"` | `ui` or `api` when the controller wrote the version for its caller, `subscription` or `auto-rollback` for its own; else `""` |
| `actor.verified` | bool | `true` | Always; `true` when the version has a stamp |

```cel
# Only stamped versions (refuse v0.11 clients and hand-written Bundles)
actor.verified

# People only: no agent, bot or unknown creator
actor.verified && actor.kind == "human"

# Agents of one sponsor may ship here
actor.kind != "agent" || actor.sponsor == "alice@example.com"
```

## `agent.*`

The version's agent as the registry says at this evaluation: an admin who suspends the agent
or tightens its autonomy changes its in-flight gates. It is read from status the registry's
reconcilers write: the AgentIdentity's `status.ready`, and the agent's entry in the release's
AgentIndex. For a version no agent created, every field is empty or false.

| Field | Type | Example | When populated |
|---|---|---|---|
| `agent.registered` | bool | `true` | The agent has a ready AgentIdentity and an entry in the AgentIndex |
| `agent.name` | string | `"fixer"` | `actor.agent` |
| `agent.sponsor` | string | `"alice@example.com"` | The sponsor now, when registered; else `""` |
| `agent.autonomy` | string | `"approve"` | `auto`, `approve` or `deny` now, when registered; else `""` |
| `agent.suspended` | bool | `false` | `true` while the AgentIdentity is suspended |

```cel
# Hold a version whose agent was suspended or deregistered after it was created
actor.kind != "agent" || (agent.registered && !agent.suspended)

# Agents with autonomy auto ship here; others wait (pair with approval.requiredWhen)
actor.kind != "agent" || agent.autonomy == "auto"
```

## `attestations.*`

New in v0.13.0: v0.12.0 has no `attestations`.

What the version's ImageVerification verified ([Attestations](../image-verification.md#attestations)):
its `status.attestations`, read when the expression names `attestations`. With several images, each
name is folded over them: verified when every image's verified, the lowest SLSA level, the summed
counts, the oldest scan, and the first image's signer, source and predicate. A version whose
Pipeline has no attestation policy has `attestations.verified == false` and no names, so an
expression that reads a name is an evaluation error and the gate blocks (fail closed).

| Field | Type | Example | When populated |
|---|---|---|---|
| `attestations.verified` | bool | `true` | Always; the version's ImageVerification is Verified |
| `attestations.<name>.verified` | bool | `true` | Each policy name; every image has a verified attestation |
| `attestations.<name>.signer` | string | `"release"` | The key authority's name or the certificate identity, when every image has the same one |
| `attestations.<name>.agent` | string | `"builder-bot"` | The [AgentIdentity](../actors.md#signing-identities) whose `signingIdentities` match the keyless signer, when exactly one does and every image has the same one |
| `attestations.<name>.predicateType` | string | `"https://slsa.dev/provenance/v1"` | Each policy name |
| `attestations.<name>.slsa.buildLevel` | int | `3` | With `slsa`; 0 to 3, the lowest image's |
| `attestations.<name>.slsa.levelSource` | string | `"builder"` | `builder` or `vsa`, when every image has the same one |
| `attestations.<name>.slsa.builderID` | string | `"https://github.com/slsa-framework/..."` | SLSA provenance, when every image has the same one |
| `attestations.<name>.slsa.sourceCommit` | string | `"1a2b3c4d..."` | The provenance's `gitCommit` dependency, when every image has the same one |
| `attestations.<name>.slsa.sourceURI` | string | `"git+https://github.com/myorg/app"` | Its URI, when every image has the same one |
| `attestations.<name>.effective.critical` | int | `0` | A scan; also `high`, `medium`, `low`, `unknown`; after VEX |
| `attestations.<name>.before.critical` | int | `2` | A scan; before VEX |
| `attestations.<name>.effectiveIDs` | list | `["CVE-2026-1234"]` | A scan; effective critical and high IDs, at most 50 |
| `attestations.<name>.scannedAt` | string | `"2026-10-14T09:00:00Z"` | A scan; RFC 3339 |
| `attestations.<name>.stale` | bool | `false` | Each policy name; `true` when any image's result is past the policy's [`maxAge`](../image-verification.md#attestation-age); always `false` without `maxAge` |
| `attestations.<name>.staleAt` | string | `"2026-10-15T09:00:00Z"` | With `maxAge`: when the first image's result goes stale, RFC 3339 |
| `attestations.<name>.scanner` | string | `"trivy"` | A scan, when every image has the same one |
| `attestations.<name>.predicate` | string | `'{"result":"PASSED"}'` | Canonical JSON, at most 64 KiB; read it with `json.unmarshal(...)`; when every image has the same one |
| `attestations.<name>.images` | list | `[{"image": "ghcr.io/myorg/app@sha256:...", "verified": true, ...}]` | Each image's own result: `image`, `verified`, `stale`, and `staleAt`, `signer`, `agent`, `authority`, `digest`, `predicate`, `slsa`, `vulns` as above, when it has them |

A version with several images folds them into one value per name: `verified` when every
image is, the lowest build level, the summed counts and the oldest scan. A field that names one
image's attestation (its signer, digest, predicate, builder, source, level source or scanner)
is set only when every image has the same value. Otherwise it is left out, and an expression
that reads it fails, so the gate blocks rather than deciding on one image. To check each image,
use `images`: `attestations.provenance.images.all(i, i.slsa.sourceURI == "git+https://github.com/myorg/app")`.

A scan's fields are also under `attestations.<name>.vulns` (`attestations.vulns.vulns.effective.high`).

```yaml
# Level-3 provenance built from the version's own commit
expression: 'attestations.provenance.slsa.buildLevel >= 3 && attestations.provenance.slsa.sourceCommit == bundle.provenance.commitSHA'
```

```yaml
# No critical and at most two high vulnerabilities after VEX
expression: 'attestations.vulns.effective.critical == 0 && attestations.vulns.effective.high <= 2'
```

```yaml
# A scan no older than its policy's maxAge
expression: '!attestations.vulns.stale && attestations.vulns.effective.high == 0'
```

```yaml
# Every image's test-result attestation passed
expression: 'attestations.tests.images.all(i, has(i.predicate) && json.unmarshal(i.predicate).result == "PASSED")'
```

## `commit.*`

New in v0.13.0: v0.12.0 has no `commit`.

The config commit's signature as the version's ImageVerification verified it
([Signed commits](../image-verification.md#signed-commits)): its `status.commit`, read when the
expression names `commit`. A version whose Pipeline does not check commits, or with no
ImageVerification yet, has `commit.verified == false` and every string empty. Every field is
always present, so an expression that reads one never fails; check `commit.verified`.

| Field | Type | Example | When populated |
|---|---|---|---|
| `commit.verified` | bool | `true` | Always; the commit's signature verified |
| `commit.method` | string | `"gitsign"` | `scm` (the SCM verified it) or `gitsign` (kardinal verified a Sigstore signature against `commits.authorities`); `""` before a check |
| `commit.signer` | string | `"https://github.com/myorg/agent/.github/workflows/fix.yml@refs/heads/main"` | The SCM's verified signer (login or email), or the certificate identity for `gitsign` |
| `commit.issuer` | string | `"https://token.actions.githubusercontent.com"` | `gitsign`: the certificate's OIDC issuer; else `""` |
| `commit.authority` | string | `"agents"` | `gitsign`: the `commits.authorities` entry that verified it; else `""` |
| `commit.agent` | string | `"fixer"` | `gitsign`: the [AgentIdentity](../actors.md#signing-identities) whose `signingIdentities` match the signer, when exactly one ready, unsuspended one does; else `""` |

The agent is read once, when the signature verifies. To hold a version whose agent was suspended
since, combine it with [`agent.*`](#agent), which the registry gives at each evaluation.

```yaml
# An agent's version: its commit was signed by that agent's own workload
expression: 'actor.kind != "agent" || (commit.verified && commit.agent == actor.agent)'
```

```yaml
# The commit was signed with gitsign by a release workflow
expression: 'commit.verified && commit.method == "gitsign" && commit.signer.startsWith("https://github.com/myorg/")'
```
