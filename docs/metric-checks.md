# Metric Checks

A `MetricCheck` runs one query against a metrics backend at a fixed interval, compares the value
with a threshold, and writes `Pass` or `Fail` to its status. A [PolicyGate](policy-gates.md) reads
the result as `metrics.<name>.result`, `.value` and `.stale`, so a metric can hold a promotion.

| Provider | Backend | Query |
|---|---|---|
| `prometheus` (default) | Prometheus HTTP API, and anything that speaks it (Thanos, Mimir, Amazon Managed Service for Prometheus) | PromQL |
| `datadog` | Datadog metrics query API (`/api/v1/query`) | Datadog metrics query |
| `cloudwatch` | Amazon CloudWatch `GetMetricData` | Metric math, `SEARCH`, or a Metrics Insights `SELECT` |
| `newrelic` | New Relic NerdGraph | NRQL |
| `web` | Any HTTP API that answers JSON | none: the response is read with JSONPath |

Every provider has to return a single value: one series (the latest point is used), one NRQL row,
or one JSONPath match. Anything else, an HTTP error, or a timeout gives `Fail`, so a gate on
`metrics.<name>.result == "Pass"` blocks (fail closed). So does a value that is not a finite
number (`NaN`, `+Inf`, `-Inf`, for example a division by zero), whatever the operator.

## Common fields

```yaml
apiVersion: kardinal.io/v1alpha1
kind: MetricCheck
metadata:
  name: error-rate          # gates read metrics["error-rate"]
  namespace: my-app         # the Pipeline namespace (an org policy namespace for org gates)
spec:
  provider: prometheus
  prometheusURL: http://prometheus.monitoring.svc:9090
  query: sum(rate(http_requests_total{job="my-app",code=~"5.."}[5m])) / sum(rate(http_requests_total{job="my-app"}[5m]))
  threshold:
    operator: lt            # lt, gt, lte, gte, eq, ne
    value: 0.01             # passes when value < 0.01
  interval: 1m              # default 1m, minimum 10s
```

- `threshold.text` compares the value as a string instead of a number, with `eq` or `ne`. Use it
  with the `web` provider, for example `{operator: eq, text: "healthy"}`.
- `status.lastValue`, `status.result`, `status.reason`, `status.lastEvaluatedAt` and
  `status.validUntil` show the last evaluation. A result is stale after three intervals (at least
  30 seconds) without a refresh, and gates then read it as `"Stale"` (see
  [Stale metric results](policy-gates.md#stale-metric-results)).
- `suspend: true` stops the queries. The last result goes stale at `validUntil`, so gates that read
  it block.
- Queries are rationed: at most one query per namespace and six in the cluster run at once, in
  first-come order; a MetricCheck waiting for a slot shows `WaitingForSlot` and keeps its last
  result, which goes stale as usual. A namespace with slow endpoints delays only its own checks.

## Credentials

Credentials are never written in a MetricCheck. Each provider names keys of a Secret in the
MetricCheck's namespace (`*SecretRef: {name, key}`). The Secret must carry the label
`kardinal.io/referenceable: "true"`; without it the check fails with
`SecretNotReferenceable: secret "<name>" does not have the label kardinal.io/referenceable: "true"`
and nothing is sent (see [Secrets that kardinal may send](guides/security.md#secrets-that-kardinal-may-send)).

```bash
kubectl -n my-app create secret generic datadog --from-literal=api-key=... --from-literal=app-key=...
kubectl -n my-app label secret datadog kardinal.io/referenceable=true
```

The controller reads the Secret at each
evaluation, so a rotated Secret is used from the next one; surrounding whitespace (a trailing
newline) is dropped. A missing Secret or key fails the check with
`secret "<name>" not found` or `secret "<name>" has no key "<key>"`. The status never shows a
credential, a URL, or a response body: only the HTTP status and the service's own error text.

The label is the Secret owner's consent: the controller can read every Secret, and a MetricCheck
sends its credentials to a URL whoever creates the MetricCheck chooses, so without the label anyone
with `create` on MetricChecks could send any Secret of the namespace to their own server.

## Providers

### Prometheus

```yaml
spec:
  provider: prometheus
  prometheusURL: https://prometheus.example.com/prefix   # the query goes to <url>/api/v1/query
  query: up{job="my-app"}
  prometheus:
    authorizationSecretRef: {name: prom-auth, key: authorization}   # "Bearer <token>" or "Basic <base64>"
  threshold: {operator: gte, value: 1}
```

`prometheus.authorizationSecretRef` is optional; without it no `Authorization` header is sent.

### Datadog

```yaml
spec:
  provider: datadog
  query: avg:trace.http.request.errors{service:my-app,env:prod}.as_rate()
  datadog:
    site: datadoghq.eu          # default datadoghq.com; the API is https://api.<site>
    window: 5m                  # the query covers [now-window, now]; default 5m
    apiKeySecretRef: {name: datadog, key: api-key}
    applicationKeySecretRef: {name: datadog, key: app-key}
  threshold: {operator: lt, value: 0.01}
```

The value is the latest non-null point of the one series the query returns. `datadog.address`
replaces `https://api.<site>`, for example with a proxy.

### CloudWatch

```yaml
spec:
  provider: cloudwatch
  query: SELECT AVG(Errors) FROM SCHEMA("AWS/Lambda", FunctionName) WHERE FunctionName = 'my-app'
  cloudWatch:
    region: eu-west-1
    period: 60                  # seconds per point, default 60
    window: 5m                  # default 5m
    accessKeyIDSecretRef: {name: aws, key: access-key-id}
    secretAccessKeySecretRef: {name: aws, key: secret-access-key}
    # sessionTokenSecretRef: {name: aws, key: session-token}   # temporary credentials
  threshold: {operator: lt, value: 1}
```

The query is a `GetMetricData` expression; it must return one result, and the value is its latest
point. `cloudWatch.endpoint` replaces `https://monitoring.<region>.amazonaws.com`, for example
with a VPC endpoint. The request is signed with SigV4. With the controller's own identity (below)
an `endpoint` must be `https://monitoring[-fips].<region>.amazonaws.com[.cn]` or its VPC endpoint
(`vpce-...monitoring.<region>.vpce.amazonaws.com[.cn]`), so the controller's session token is never
sent anywhere else.

Without the two Secret refs the check fails, unless the controller may use its own AWS identity:
install with `--set metricCheck.cloudWatch.ambientCredentials=true` (controller flag
`--metriccheck-cloudwatch-ambient-credentials`) and give the controller's ServiceAccount an IAM
role (IRSA annotation `eks.amazonaws.com/role-arn`, or EKS Pod Identity) that allows
`cloudwatch:GetMetricData`. It is off by default: with it, anyone who can create a MetricCheck can
read CloudWatch metrics with the controller's role.

### New Relic

```yaml
spec:
  provider: newrelic
  query: SELECT percentage(count(*), WHERE error IS true) FROM Transaction WHERE appName = 'my-app' SINCE 5 minutes ago
  newRelic:
    accountID: 1234567
    region: US                  # US (default) or EU
    apiKeySecretRef: {name: newrelic, key: user-api-key}
    # resultField: percentage   # needed when the row has more than one field
  threshold: {operator: lt, value: 1}
```

The NRQL runs through NerdGraph (`actor.account.nrql`), passed as a GraphQL variable. It must
return one row (no `FACET` or `TIMESERIES`); the value is its only field, or `resultField`.
`newRelic.address` replaces the NerdGraph URL.

### Web (any JSON API)

The `web` provider calls an HTTP endpoint and reads one value from the JSON response with a
kubectl-style JSONPath expression. It is the HTTP check for systems without a metrics API: a
release-readiness service, a test runner, a feature-flag service, a ticketing system.

```yaml
spec:
  provider: web
  web:
    url: https://quality.example.com/api/releases/{{ bundle.version }}/status
    method: GET                 # GET (default) or POST
    headers:
      - name: Authorization
        valueFromSecret: {name: quality-api, key: authorization}
      - name: X-Environment
        value: prod
    # body: '{"version": "{{ bundle.version }}"}'   # for POST, sent as application/json
    jsonPath: '{.checks.integration.state}'
    timeoutSeconds: 10          # default 10, at most 60
  threshold:
    operator: eq
    text: passed
```

The check fails unless the response status is 2xx (redirects are not followed) and `jsonPath`
selects exactly one string, number or boolean. Limits, so one slow or large endpoint cannot hold
the controller: the response may be at most 64 KiB, recursive descent (`..`) is not supported, a
request never takes longer than half of `interval`, and at most two web checks run at once (one
that finds no slot is retried 2 seconds later). A JSONPath that selects nothing reports
`selected nothing`, never the document. A number, or a string that parses as one, can be
compared with `threshold.value`; a string or boolean (`"true"`, `"false"`) with `threshold.text`.
No CEL runs on the response.

## Per-promotion analysis

A MetricCheck with `spec.perPromotion: true` is a template: it is not queried itself. When a
Bundle's Graph is built, every gate of the Pipeline namespace that reads `metrics.<template name>`
gets an instance of it for its environment: a MetricCheck that the Graph owns, named
`<template>-<environment>--<bundle>`, with these placeholders replaced in `query`, `web.url`,
`web.body` and `web.headers[].value`:

| Placeholder | Value |
|---|---|
| `{{ bundle.name }}` | The Bundle's name |
| `{{ bundle.version }}` | `bundle.version` as gates see it: the first image tag, or the first 8 characters of `configRef.commitSHA` for a config Bundle |
| `{{ bundle.imageTag }}` | The first image's tag |
| `{{ bundle.imageDigest }}` | The first image's digest |
| `{{ bundle.commitSHA }}` | `provenance.commitSHA` |
| `{{ pipeline.name }}` | The Pipeline's name |
| `{{ environment.name }}` | The gated environment |
| `{{ namespace }}` | The Pipeline's namespace |

The gate of that Bundle and environment reads the instance's result as `metrics.<template name>`,
so the same gate expression analyses each promotion on its own:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: MetricCheck
metadata:
  name: canary-errors
  namespace: my-app
spec:
  provider: datadog
  perPromotion: true
  query: avg:trace.http.request.errors{service:my-app,version:{{ bundle.version }},env:uat}.as_rate()
  datadog:
    apiKeySecretRef: {name: datadog, key: api-key}
    applicationKeySecretRef: {name: datadog, key: app-key}
  threshold: {operator: lt, value: 0.01}
  interval: 30s
---
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: canary-errors-ok
  namespace: my-app
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: metrics["canary-errors"].result == "Pass"
```

How the Graph runs it:

- The instance is created once every upstream environment of the gated one is Verified, so the
  analysis starts when there is a deployment to analyse. Until then the gate sees the template,
  which has no result, and blocks.
- Once the Bundle is no longer promoting (Verified, Failed or Superseded) the Graph sets the
  instance's `spec.suspend`, and it stops querying. A Failed Bundle that promotes again resumes it.
- A value with a character outside `A-Z a-z 0-9 . _ + -` (a digest such as `sha256:<hex>`
  excepted), an empty value, a value that starts with `.`, and a value containing `..` are not put
  into a query (a Bundle's tag comes from CI, and
  `"} or vector(1)` would rewrite a PromQL query; an empty commit matches nothing, and a count over
  nothing is 0, which would pass). The placeholder stays, and the instance fails with
  `unrendered placeholder {{ ... }}`. Bundle image tags must also follow the OCI tag grammar.
- In `web.url` the scheme and host must be literal: a placeholder in the host, or a rendered URL
  whose scheme, host or port differs from the template's, leaves the URL unrendered, and the
  instance fails.
- Put placeholders inside quoted literals (`version="{{ bundle.version }}"`,
  `WHERE version = '{{ bundle.version }}'`), never inside a regular expression (`=~`, `LIKE`,
  `RLIKE`): there `.` and `+` are operators, and a value can match more than its own release. An unknown placeholder does the same,
  and the template's `status.reason` names it. A MetricCheck that is not a template and contains a
  placeholder fails the same way.
- Templates are read when the Bundle's Graph is built: a template created afterwards applies to
  the next Bundle. Instances are deleted with the Bundle's Graph.
- Org gates read the MetricChecks of their org policy namespace, where the Graph creates nothing:
  a template there has no result and an org gate that reads it blocks. Use per-promotion templates
  with gates of the Pipeline namespace.

## Outbound requests

Every URL (Prometheus, Datadog, CloudWatch, New Relic, web) goes through the egress guard: a
loopback, link-local or cloud metadata address is refused and the reason reads
`destination address is not allowed` (see
[Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls)). Private
addresses (in-cluster Services) are allowed. With `networkPolicy.enabled`, add the backends to
`networkPolicy.extraEgress`.
