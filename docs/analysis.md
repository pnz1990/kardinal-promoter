# Analysis with Argo Rollouts AnalysisTemplates

An environment can verify each promotion with [Argo Rollouts analysis](https://argo-rollouts.readthedocs.io/en/stable/features/analysis/):
after its health check passed, kardinal runs an `AnalysisRun` for each `AnalysisTemplate` you
name, and the environment is Verified, and the next one starts, only when every run is
`Successful`. Every Argo Rollouts provider works: Prometheus, Datadog, New Relic, CloudWatch,
Wavefront, Graphite, InfluxDB, Kayenta, a web request, a Kubernetes Job, and the plugins.

Argo Rollouts must be installed (its controller and CRDs); the environment's workload does not
have to be a Rollout.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: success-rate
  namespace: my-app            # the Pipeline's namespace
spec:
  args:
    - name: service
    - name: tag                # kardinal fills it in: the Bundle's image tag
  metrics:
    - name: success-rate
      interval: 1m
      count: 5
      successCondition: result[0] >= 0.99
      provider:
        prometheus:
          address: http://prometheus.monitoring:9090
          query: |
            sum(rate(http_requests_total{service="{{args.service}}",version="{{args.tag}}",code!~"5.."}[2m]))
            / sum(rate(http_requests_total{service="{{args.service}}",version="{{args.tag}}"}[2m]))
---
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
  namespace: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
  environments:
    - name: staging
    - name: prod
      verification:
        analysisTemplates:
          - name: success-rate
          - name: slo-burn            # cluster-wide template
            kind: ClusterAnalysisTemplate
        args:
          - name: service
            value: my-app-prod
        timeout: 20m
```

## Fields

`spec.environments[].verification`:

| Field | Required | Default | Description |
|---|---|---|---|
| `analysisTemplates[].name` | Yes | | Template name. 1 to 10 templates, each run as its own AnalysisRun. |
| `analysisTemplates[].kind` | No | `AnalysisTemplate` | `AnalysisTemplate` (in the Pipeline's namespace) or `ClusterAnalysisTemplate`. |
| `args[]` | No | | `{name, value}`: values for the args the templates declare. |
| `inconclusive` | No | `fail` | What an `Inconclusive` run counts as: `fail` or `pass`. |
| `timeout` | No | `30m` | Go duration from the moment the environment entered `Verifying`. A run still going then fails the environment. Bound your metrics with `count` so a run ends by itself. |

## Args

Each AnalysisRun gets one arg for every arg its template declares, valued from, in order:

1. `verification.args`;
2. what kardinal knows about the promotion: `bundle`, `pipeline`, `environment`, `image` (the
   Bundle's first image, `repository:tag` or `repository@digest`), `tag`, `digest`, and `commit`
   (the config Bundle's commit, else `provenance.commitSHA`);
3. the template's own `value` or `valueFrom` (for example a `secretKeyRef` holding an API key).

An arg left with no value fails the run: Argo Rollouts reports `args.<name> was not resolved`.

### Trust

The built-in values come from the Bundle, which CI, a Subscription or anyone allowed to create
Bundles writes; the AnalysisTemplate, written by whoever may create templates in the namespace,
interpolates them into queries, URLs and Job commands. So kardinal passes a built-in value only
when it matches its grammar, and otherwise fails the Bundle before any environment
(`InvalidSpec`, `GraphBuildFailed`, `arg tag: ...`):

| Arg | Grammar |
|---|---|
| `tag` | `^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$` (OCI tag) |
| `digest` | `^sha256:[a-f0-9]{64}$` |
| `commit` | `^[a-f0-9]{7,64}$` |
| `image` | an OCI repository reference, plus a valid tag or digest |
| `bundle`, `pipeline`, `environment` | Kubernetes names (the API server checks them) |

A template that does not declare the arg is not affected. `verification.args` values are
written by the Pipeline's author and passed as they are. Templates should still quote what
they interpolate where the provider allows it.

**Who can write the verdict.** The step reads the phase of the AnalysisRuns its Graph rendered
and nothing else: an AnalysisRun counts only when its name is one this build of the Graph
rendered (a list rebuilt at every translation), kro applied it (label `kro.run/node-id`) and it
belongs to this Bundle (label `kardinal.io/bundle-uid` equal to the Bundle's UID). So an
AnalysisRun someone creates with matching `kardinal.io/*` labels is ignored. That does not make
the verdict tamper-proof: Argo Rollouts aggregates `create` and `patch` on `analysisruns` into
the built-in `edit` and `admin` ClusterRoles, and its controller trusts `status`. Anyone who can
write AnalysisRuns in the Pipeline namespace (namespace editors, by default) can patch the real
run's status to `Successful`, or edit its metrics before it measures, and so decide whether the
environment is verified. Treat `analysisruns` write access in Pipeline namespaces like Pipeline
edit access, and remove it from roles that must not affect promotions.

## How it runs

1. The health check (and any `bake` window) passed: the step enters **`Verifying`**, together
   with any [post-deploy hooks](hooks.md).
2. The Bundle's Graph creates one AnalysisRun per template, named
   `<pipeline>-<bundle>-<env>-<template>-<hash>` and labelled `kardinal.io/bundle`,
   `kardinal.io/environment` and `kardinal.io/analysis-template`. The Argo Rollouts controller
   runs it.
3. The Graph copies each run's `status.phase` onto the step (`spec.live.analyses`). The step
   waits per template, on the newest run of each (see [Template changes](#template-changes)):
    - every run `Successful` (and every post hook succeeded): the step is `Verified`, condition
      reason `VerificationSucceeded`;
    - a run `Failed`, `Error`, or `Inconclusive` with `inconclusive: fail`, or the timeout passed:
      the environment's `onHealthFailure` applies, as for a failed health check (`none` fails the
      step and the Bundle, `abort` stops it for a human, `rollback` rolls the environment back).
      The change is already deployed when the analysis runs.
4. A run nothing waits for any more is terminated (`spec.terminate: true`, so Argo Rollouts stops
   measuring): when the step failed (a failed run or hook, the timeout), was aborted or rolled
   back, or the Bundle was superseded.

`inconclusive` and `timeout` are copied into the step when its Graph is built
(`spec.analysisPolicy`): a Pipeline edit applies to the next Bundle, not to a step already
verifying.

```bash
kubectl get analysisruns -n my-app -l kardinal.io/environment=prod
kubectl get promotionstep -n my-app my-app-my-app-x7k2p-prod -o jsonpath='{.spec.live.analyses}'
```

## Verification fails closed

kardinal never promotes past an environment whose analysis it cannot run:

- **Argo Rollouts not installed** (the cluster does not serve `argoproj.io/v1alpha1`
  `AnalysisRun`): every Bundle that promotes through the environment fails before its first
  environment, with condition `InvalidSpec` (reason `GraphBuildFailed`) and the message
  `... AnalysisRun is not served by the cluster: install Argo Rollouts (its CRDs) or remove
  spec.verification`. Health checks are different: they skip a kind the cluster does not serve.
- **A template that does not exist**: the Bundle fails the same way, naming the template.

After installing Argo Rollouts or creating the template, apply the Pipeline again (any change)
or create a new Bundle.

## Template changes

kardinal reads the templates when it builds the Bundle's Graph and copies their metrics into the
AnalysisRuns, as it copies PolicyGate templates into gate instances: an edit to a template applies
to the next Bundle, or to the Bundle in flight when the Pipeline changes. The run's name carries a
hash of its spec, so an edit picked up mid-flight starts a new run instead of changing a run in
progress. The step then waits for the new run (the newest run of the template) and says so in its
message; the timeout keeps counting from when the step entered `Verifying`, it does not restart.

## What it cannot do

- Verification needs the node Graph shape. A Pipeline whose Bundles get a
  [compact Graph](pipeline-reference.md#large-pipelines) (more than `--graph-compact-above`
  environments, default 100, or the annotation `kardinal.io/graph-shape: compact`) is
  `Ready=False`, and its Bundles fail with `GraphBuildFailed` naming the analysis instead of
  promoting unverified.
- The AnalysisRuns run in the Pipeline's namespace on the cluster kardinal runs in. The Argo
  Rollouts controller must watch that namespace (not run with `--namespaced` elsewhere).
- An AnalysisRun deleted by hand is created again by the Graph and runs again.
- A Bundle superseded while its analysis runs starts no new run; the running one is terminated.

## Permissions

The Graph ServiceAccount creates and reads `analysisruns` (the chart's
`<fullname>-graph-applier` ClusterRole), kro's aggregated role watches them, and the controller
reads `analysistemplates` (namespaced) and `clusteranalysistemplates` by name. See
[Graph coverage](graph-coverage.md) and ledger entries
[G5](design/16-graph-capability-ledger.md#g5-the-graph-identity-is-provisioned-outside-the-graph)
(why templates are copied, not read by the Graph) and
[G14](design/16-graph-capability-ledger.md#g14-a-node-with-one-pending-field-is-wholly-unresolved)
(the mirror).
