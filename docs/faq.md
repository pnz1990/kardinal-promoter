# FAQ

Frequently asked questions about kardinal-promoter.

---

## General

### How does kardinal-promoter differ from Kargo?

Both tools promote changes between environments on Kubernetes. kardinal models a pipeline
as a DAG of environments, gates promotions with CEL PolicyGates, writes promotion evidence into
each PR, and works with Argo CD, Flux or plain Deployments.

For a full feature comparison, see [Comparison](comparison.md).

### Can I use kardinal-promoter without ArgoCD?

Yes. kardinal-promoter opens Git pull requests and checks Kubernetes `Deployment` readiness
by default. ArgoCD and Flux integrations are optional. You can use:

- **Deployment readiness** (`health.type: resource`, the default; no GitOps engine needed)
- **Argo CD Application** (`health.type: argocd`)
- **Flux Kustomization** (`health.type: flux`)
- **Argo Rollouts Rollout** (`health.type: argoRollouts`)
- **Flagger Canary** (`health.type: flagger`)

See [Health Adapters](health-adapters.md) for configuration.

### Does it work with GitLab?

Yes. Start the controller with `--scm-provider gitlab`
(Helm value `scm.provider: gitlab`). One controller serves one SCM for every Pipeline;
`spec.git.provider` is deprecated and ignored. Forgejo, Gitea, Bitbucket Cloud and Azure DevOps
are also supported. See [SCM Providers](scm-providers.md).

### Can I use it with Helm?

Yes. Set `update: {strategy: helm}` on the Pipeline environment. kardinal
will update the `image.tag` (or a custom path) in `values.yaml` instead of Kustomize
overlays. It writes one image per Bundle: a Bundle with more than one tagged image
fails the step. Use one Bundle per chart image, or kustomize.

---

## Installation and Configuration

### What are the minimum cluster requirements?

- Kubernetes 1.30 or later. kro's CRDs need it, and `hack/install-kro.sh` and the chart refuse
  older clusters; see [Kubernetes version](installation.md#kubernetes-version). CI runs the live
  e2e suites on the three newest minors, currently 1.35, 1.36 and 1.37.
- [kro](installation.md#install-kro) v0.10.0-rc.0+ with the Graph controller (`GraphKind` feature gate)
- An SCM token: for GitHub, a token with `repo` scope; for GitLab, one with `api` scope. See
  [SCM Providers](scm-providers.md) for the other providers.

### What permissions does the controller need?

The Helm chart creates the necessary `ClusterRole` (a `Role` in namespace mode). It grants:

- `get/list/watch/create/update/patch/delete` on the `kardinal.io` kinds, except
  `auditevents` (`get/list/watch/create`, plus `delete` when you turn on retention with
  `audit.retention.enabled: true`: the controller never changes a record) and the
  cluster-scoped `changewindows` (`get/list/watch`, in a `ClusterRole` in both modes)
- `get/update/patch` on the `status` subresources of those kinds (`auditevents` has none)
- `get/list/watch/create/update/patch/delete` on `graphs.kro.run`
- `get/list/watch` on the health targets: `deployments`, Argo CD `applications` and `rollouts`,
  Flux `kustomizations`, Flagger `canaries` (`patch` on `applications` only with
  `rbac.argocdApplicationsWrite`), `get` on `replicasets` (the ReplicaSet a Deployment's
  `ProgressDeadlineExceeded` names), and `list` on `pods` (the new ReplicaSet's pods, to name
  why one is not ready; cluster-wide in cluster mode)
- `get` on `secrets` (Pipeline `spec.git.secretRef` and the SCM token); no `list` or `watch`
- `get/create` on `serviceaccounts` and `get/list/create/update/delete` on `rolebindings`, plus
  `bind` on the two Graph ClusterRoles only, for the [Graph identity](installation.md)
- `get` on `namespaces` (only the watched one in namespace mode), to tell whether a Graph's,
  Bundle's or step's namespace is being deleted
- `create/patch` on `events.k8s.io` `events` and `get/list/watch/create/patch` on core `events`
- in the release namespace: the leader election `leases`, and `create` plus
  `get/update/patch` on the `kardinal-version` ConfigMap
- `create` on `tokenreviews` and `subjectaccessreviews`, only with `ui.auth.tokenReview`

It grants nothing on `pods`, `services`, other ConfigMaps or `batch` Jobs.

See [Security Guide](guides/security.md) for a full RBAC manifest.

### How do I configure a GitHub token?

Create a Kubernetes Secret with the token, then reference it in the Helm values:

```bash
kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=ghp_your_token
```

```yaml
# values.yaml
github:
  secretRef:
    name: github-token
    key: token
```

Never put the token directly in `values.yaml` for production clusters.

### How many replicas should I run?

One replica is sufficient for most clusters. Leader election is enabled by default
(`leaderElect: true`), so you can safely run 2 for HA. The second replica stays in
standby and takes over if the primary crashes. A leader that shuts down (a rollout or a
node drain) releases its lease, so the standby takes over within seconds; after a crash
it waits for the lease to expire (15 seconds).

---

## Operations

### What happens if the controller restarts mid-promotion?

Nothing is lost. All state is persisted in CRDs. When the controller restarts, it
reconciles all `PromotionStep` and `PolicyGate` CRs and resumes from where they left off.
Each reconciler is idempotent and safe to re-run after a crash.

### How do I debug a stuck bundle?

1. **Check the Bundle status**:
   ```bash
   kubectl get bundle <name> -o yaml | grep -A 20 status
   ```

2. **Check PromotionSteps**:
   ```bash
   kardinal get steps <pipeline>
   ```

3. **Check PolicyGates**:
   ```bash
   kardinal explain <pipeline> --env <env>
   ```

4. **Check the Graph**:
   ```bash
   kubectl get graph -l kardinal.io/bundle=<bundle-name> -o yaml
   ```

5. **Check controller logs**:
   ```bash
   kubectl logs -n kardinal-system deployment/kardinal-promoter -f
   ```

### How do I manually approve a blocked bundle?

Force-pass the blocking PolicyGate with `kardinal override`. It records the
reason and expiry on the gate; the Kubernetes audit log records who made the
change. (The override's `createdBy` is the local OS user name the CLI sends,
and anyone who can edit the gate can set it to any value, so do not treat it as
an identity.)

```bash
kardinal override <pipeline> --stage prod --gate <gate-name> \
  --reason "hotfix for INC-123" --expires-in 1h
```

`kardinal approve` is deprecated: the label it set was never read by any gate,
so it bypassed nothing. It now exits with an error that points to
`kardinal override`. See [kardinal override](reference/cli/kardinal-override.md).

### How do I pause a promotion mid-flight?

```bash
kardinal pause <pipeline>
```

No new promotion step starts, and a step that has not opened its PR yet holds
before its next git step. Steps waiting for a PR merge or running health checks
finish, and open PRs are not closed. Resume with `kardinal resume <pipeline>`;
held steps continue where they stopped. See [Pause and Resume](rollback.md#pause-and-resume).

### What triggers a rollback?

Rollbacks are triggered:

1. **Manual**: `kardinal rollback <pipeline> --env prod` — creates a rollback Bundle. In a
   `pr-review` environment it opens a PR labelled `kardinal/rollback`; in an `auto` environment
   it commits and pushes straight to the branch, the same as any promotion there, with no PR
   (with `update.strategy: argocd` it sets the image on the Argo CD Application instead)
2. **Automatic**: the environment sets `onHealthFailure: rollback` and its health check fails.
   That means a terminal result (a Deployment `ProgressDeadlineExceeded` from this promotion's
   rollout, a failed Flagger canary), no healthy result before `health.timeout`, or an unhealthy
   check during a `bake` window with `policy: fail-on-alarm`. A rollback Bundle that fails its own
   health check is not rolled back again; its step ends `AbortedByAlarm`.
   See [Rollback](rollback.md#automatic-rollback).
   (`environments[].autoRollback` is not implemented and is rejected by the API server.)

A rollback is a forward promotion of the previously-verified Bundle image through the
same pipeline, same gates, same audit trail.

### How do I see what changed between two promotions?

```bash
kardinal diff <bundle-a> <bundle-b>
```

Compares the images (matched by repository) and provenance of two Bundles.
`kardinal get bundles <pipeline>` lists the Bundle names.

---

## Policy Gates

### Can I write a gate that allows hotfixes to bypass the weekend block?

Yes. Combine conditions:

```yaml
spec:
  expression: >
    !schedule.isWeekend ||
    ("kardinal.io/hotfix" in bundle.labels && bundle.labels["kardinal.io/hotfix"] == "true")
```

Create the Bundle with `kubectl apply` and set `kardinal.io/hotfix: "true"` in `metadata.labels`.
`kardinal create bundle` and the Bundle API do not set custom labels.
Bundle annotations are not in the CEL context; labels are (`bundle.labels`).

### How often does kardinal re-evaluate a gate?

The `recheckInterval` field on the PolicyGate spec controls this (default: `5m`).
The PolicyGateReconciler re-runs the CEL expression on that schedule. When a gate
transitions from blocked to allowed, the Graph controller immediately advances.

### Can I test a gate without deploying?

```bash
kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"
# RESULT: BLOCKED
# Blocked by: no-weekend-deploys
# Message: "Production deployments are blocked on weekends"
# Next window: Monday 00:00 UTC
#
# no-weekend-deploys:   BLOCK   (!schedule.isWeekend = false)

kardinal policy test my-gate.yaml
# PolicyGate "no-weekend-deploys" (my-gate.yaml):
#   Expression: !schedule.isWeekend
#   Syntax: valid
#   Result: PASS (!schedule.isWeekend = true)
#
# All gates valid and pass current context (1 gate(s))
```

---

## Architecture

### Why does kardinal need kro?

The kro Graph controller handles the complex part of DAG orchestration:
creating owned resources in dependency order, watching `readyWhen` conditions,
and stopping the graph on failure. kardinal reuses this instead of reimplementing it.
This keeps the kardinal controller focused on promotion-specific concerns.

### Does kardinal store state in a database?

No. All state is in Kubernetes CRDs (etcd). The controller is completely stateless
and can be deleted and recreated without data loss.

### Is kardinal-promoter production-ready?

kardinal-promoter is pre-1.0 and under active development. The CRD API may change
between minor versions. Recommended for early adopters who can tolerate migration.
Follow releases at [GitHub Releases](https://github.com/pnz1990/kardinal-promoter/releases).

---

## Contributing

### Where should I report bugs?

Open an issue at [github.com/pnz1990/kardinal-promoter/issues](https://github.com/pnz1990/kardinal-promoter/issues).

### How do I run the tests?

```bash
go test ./... -race -count=1 -timeout 120s
```
