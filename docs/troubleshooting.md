# Troubleshoot

Start with `kardinal doctor` and `kardinal why -o wide` below, then go by symptom:

- [Promotion stuck or failed](troubleshoot-promotions.md): a version that does not move, a step
  that waits, a health check or a gate that never passes, an object that does not delete.
- [Git and SCM problems](troubleshoot-git.md): clone, push, pull request and SCM API errors.

This page also has admission errors, debugging commands, RBAC checks and common questions.

## By symptom

### Promotion is stuck

A step stays in HealthChecking, a gate shows a CEL error, a step waits for its merge. See [Promotion is stuck](troubleshoot-promotions.md#promotion-is-stuck).

### Bundle not promoting

A version stays Available, or fails with "skip denied". See [Bundle not promoting](troubleshoot-promotions.md#bundle-not-promoting).

### Health adapter issues

An Application or Kustomization is not found. See [Health adapter issues](troubleshoot-promotions.md#health-adapter-issues).

### Webhook issues

A merged PR does not advance the step; the API answers 429. See [Webhook issues](troubleshoot-promotions.md#webhook-issues).

### Graph controller issues

The Graph is not accepted or not ready, or creates no steps. See [Graph controller issues](troubleshoot-promotions.md#graph-controller-issues).

### PolicyGate never becomes Ready

A gate stays Block or Waiting. See [PolicyGate never becomes Ready](troubleshoot-promotions.md#policygate-never-becomes-ready).

### A PromotionStep, Graph or namespace never finishes deleting

Finalizers that wait for a PR to close. See [A PromotionStep, Graph or namespace never finishes deleting](troubleshoot-promotions.md#a-promotionstep-graph-or-namespace-never-finishes-deleting).

### Git errors

Pushes that wait their turn, moved base branches, rebuilt PR branches. See [Git errors](troubleshoot-git.md#git-errors).

### SCM provider failures

Authentication, missing Secrets, rate limits, the circuit breaker. See [SCM provider failures](troubleshoot-git.md#scm-provider-failures).

## Admission validation errors

The kardinal CRDs validate their fields with OpenAPI schema rules, so `kubectl apply`
rejects a bad value before it is stored. The error names the field and the rule it broke.
Common cases:

| Field | Fix |
|---|---|
| `spec.environments` | Add at least one environment to your Pipeline spec |
| `spec.environments[].name` | Use a non-empty, valid environment name; the error names the rule it broke |
| `spec.environments[].update.strategy` | Only `kustomize`, `helm`, `argocd`, `yaml` are valid |
| Bundle `spec.type` | Only `image`, `config`, `mixed`, `chart` are valid |
| Bundle `spec.pipeline` | Add `pipeline: <name>` to your Bundle spec |
| PolicyGate `spec.expression` | Add a CEL expression to your PolicyGate spec |
| PolicyGate `spec.recheckInterval` | Use Go duration format: `5m`, `30s`, `1h` (not `5 minutes`) |

These checks are part of the CRDs and cannot be turned off. The chart's ValidatingAdmissionPolicies are the identity policies (a rejection, an override or an approval
must name the requesting user, see [Verified identity](guides/security.md#verified-identity)), the hold-writes policy (only `pipelines/hold` may change `spec.holds`),
the fleets policy (each `spec.pausedFleets` entry names an environment with a fleet) and the gitops-writes policy (the controller changes Argo CD and Flux objects only by their refresh annotation); an
`admission webhook "..." denied the request` or `ValidatingAdmissionPolicy '<release>-...' ... denied
request` error names the rule.

---

## Start here: `kardinal doctor`

Before diving into specific issues, run the pre-flight check to rule out common
configuration problems:

```bash
kardinal doctor
```

This checks: controller reachability (the `kardinal-version` ConfigMap), CRDs, the kro
controller and Graph CRD, and that `GITHUB_TOKEN` is set on the controller Deployment.
If any check fails, the output includes a remediation hint.

If kardinal-promoter is installed in a namespace other than `kardinal-system`, pass it:
`kardinal doctor --controller-namespace <namespace>`.

For a specific pipeline: `kardinal doctor --pipeline my-app` (in the current namespace, or `-n`).

---

## Debugging commands

```bash
# Overview of all pipelines
kardinal get pipelines

# Detailed view of steps and gates
kardinal get steps my-app

# Why is an environment blocked?
kardinal why my-app prod -o wide

# Redraw every 3 seconds until Ctrl-C
kardinal why my-app prod -o wide -w

# Bundle history and evidence
kardinal history my-app

# List all policy gates
kardinal policy list

# Validate a policy file
kardinal policy test my-gate.yaml

# Simulate a gate evaluation
kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"

# Raw CRD inspection
kubectl get kardinal-all -o wide     # every kardinal object; kubectl get kardinal: the ones you write
kubectl get graph -l kardinal.io/pipeline=my-app
```

---

## RBAC debugging

### Symptom: "forbidden: User ... cannot list resource ... in API group ..."

The controller ServiceAccount lacks a required RBAC permission.

```bash
# Check what the controller can do
kubectl auth can-i --list \
  --as=system:serviceaccount:kardinal-system:kardinal-promoter

# Check for RBAC errors in logs
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep -i "forbidden\|permission"
```

The Helm chart installs a ClusterRole with all required permissions. If you customized RBAC or installed in a restricted namespace, re-apply the Helm chart:
```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.1 \
  --namespace kardinal-system --reset-then-reuse-values
```

### Symptom: Team cannot create PolicyGates in another team's namespace

kardinal does not create RBAC for users. Your cluster's Roles decide who may write PolicyGates in each namespace. Check with:
```bash
kubectl auth can-i create policygates.kardinal.io -n platform-policies --as <user>
```

---

## Common questions

### Can I use kardinal without Argo CD?

Yes. kardinal writes to Git and checks health with a [health adapter](health-adapters.md): a
plain Deployment (`resource`, the default), an Argo CD Application, a Flux Kustomization, an Argo
Rollouts Rollout or a Flagger Canary. No GitOps engine is required.

### Does it work with GitLab, or with Helm?

Yes to both. For GitLab, start the controller with `--scm-provider gitlab`
(Helm value `scm.provider: gitlab`). A Pipeline can name another SCM with `spec.git.providerRef`
(an ScmProvider or ClusterScmProvider). Forgejo,
Gitea, Bitbucket Cloud, Bitbucket Data Center and Azure DevOps are also supported. See [SCM Providers](scm-providers.md).

For Helm, set `update: {strategy: helm}` on the Pipeline environment. kardinal
will update the `image.tag` (or a custom path) in `values.yaml` instead of Kustomize
overlays. It writes one image per version: a version with more than one tagged image
fails the step. Use one version per chart image, or kustomize.


### What does the controller need?

Kubernetes 1.35 or later, [kro](installation.md#install-kro) with the Graph controller, and an
SCM token (for GitHub, `repo` scope; for GitLab, `api` scope; see
[SCM providers](scm-providers.md)). The chart creates its RBAC; every permission is listed in
[Security](guides/security.md#every-permission-the-chart-grants). Put the token in a Secret and
name it in `scm.secretRef` ([Install](installation.md#1-create-the-github-token-secret)).

### How many replicas should I run?

One replica is sufficient for most clusters. Leader election is enabled by default
(`leaderElect: true`), so you can safely run 2 for HA. The second replica stays in
standby and takes over if the primary crashes. A leader that shuts down (a rollout or a
node drain) releases its lease, so the standby takes over within seconds; after a crash
it waits for the lease to expire (15 seconds).


### What happens if the controller restarts mid-promotion?

Nothing is lost. All state is persisted in CRDs. When the controller restarts, it
reconciles all `PromotionStep` and `PolicyGate` CRs and resumes from where they left off.
Each reconciler is idempotent and safe to re-run after a crash.

### How do I force a blocked version through?

Force-pass the blocking gate with [`kardinal override`](overrides.md). It records the reason, the
expiry and your verified Kubernetes username on the gate:

```bash
kardinal override <pipeline> --env prod --gate <gate-name> \
  --reason "hotfix for INC-123" --expires-in 1h
```

`kardinal approve` does not bypass a gate: it records your approval for an
[approval gate](approvals.md), which passes once enough allowed people approved.

### What triggers a rollback?

Rollbacks are triggered:

1. **Manual**: `kardinal rollback <pipeline> --env prod` — creates a rollback version. In a
   `pr-review` environment it opens a PR labelled `kardinal/rollback`; in an `auto` environment
   it commits and pushes straight to the branch, the same as any promotion there, with no PR
   (with `update.strategy: argocd` it sets the image on the Argo CD Application instead)
2. **Automatic**: the environment sets `onHealthFailure: rollback` and its health check fails.
   That means a terminal result (a Deployment `ProgressDeadlineExceeded` from this promotion's
   rollout, a failed Flagger canary), no healthy result before `health.timeout`, or an unhealthy
   check during a `bake` window with `policy: fail-on-alarm`. A rollback version that fails its own
   health check is not rolled back again; its step ends `AbortedByAlarm`.
   See [Rollback](rollback.md#automatic-rollback).
   (`environments[].autoRollback` is not implemented and is rejected by the API server.)

A rollback is a forward promotion of the previously-verified version image through the
same pipeline, same gates, same audit trail.

### How do I see what changed between two versions?

```bash
kardinal diff <bundle-a> <bundle-b>
```

Compares the images (matched by repository) and provenance of two versions.
`kardinal get bundles <pipeline>` lists the version names.


### Can a gate let hotfixes through the weekend block?

Yes. Combine conditions:

```yaml
spec:
  expression: >
    !schedule.isWeekend ||
    ("kardinal.io/hotfix" in bundle.labels && bundle.labels["kardinal.io/hotfix"] == "true")
```

Create the version with `kubectl apply` and set `kardinal.io/hotfix: "true"` in `metadata.labels`.
`kardinal create bundle` and the Bundle API do not set custom labels.
Bundle annotations are not in the CEL context; labels are (`bundle.labels`).

### How often is a gate re-evaluated?

The `recheckInterval` field on the PolicyGate spec controls this (default: `5m`).
The PolicyGateReconciler re-runs the CEL expression on that schedule. When a gate
transitions from blocked to allowed, the Graph controller immediately advances.

### Can I test a gate without deploying?

Yes. `kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"` evaluates the
gates of an environment at a given time, and `kardinal policy test my-gate.yaml` checks a gate file.
See [Simulating Expressions](reference/cel-context.md#simulating-expressions) and
[Testing Expressions](reference/cel-context.md#testing-expressions).


### Is kardinal-promoter production-ready?

kardinal-promoter is pre-1.0 and under active development. The CRD API may change
between minor versions. Recommended for early adopters who can absorb API changes between minor versions.
Follow releases at [GitHub Releases](https://github.com/pnz1990/kardinal-promoter/releases).


### Where do I report a bug?

Open an issue at [github.com/pnz1990/kardinal-promoter/issues](https://github.com/pnz1990/kardinal-promoter/issues).
