# FAQ

Frequently asked questions about kardinal-promoter.

---

## General

### How does kardinal-promoter differ from Kargo?

Both tools automate Kubernetes promotion pipelines. The key differences:

| Aspect | kardinal-promoter | Kargo |
|---|---|---|
| Promotion model | DAG — fan-out to parallel envs, arbitrary dependencies | Linear — envs in a fixed chain |
| Policy gates | CEL expressions with kro library (json, maps, schedule, etc.) | Basic approval-only gates |
| GitOps engine | Any (ArgoCD, Flux, raw K8s) | ArgoCD only |
| PR evidence | Structured body with image digest, CI run, gate results | None |
| Architecture | Graph-first (krocodile DAG) | Reconciler-first |

For a full feature comparison, see [Comparison](comparison.md).

### Can I use kardinal-promoter without ArgoCD?

Yes. kardinal-promoter opens Git pull requests and checks Kubernetes `Deployment` readiness
by default. ArgoCD and Flux integrations are optional. You can use:

- **Kubernetes Deployment health check** (no GitOps engine needed)
- **ArgoCD Application sync** (`health.type: argocd`)
- **Flux Kustomization ready** (`health.type: flux`)
- **Argo Rollouts** (`health.type: argo-rollouts`)

See [Health Adapters](health-adapters.md) for configuration.

### Does it work with GitLab?

Yes, GitLab SCM support is in beta. Set `scm.provider: gitlab` in the Pipeline spec.
See [SCM Providers](scm-providers.md).

### Can I use it with Helm?

Yes. Set `updateStrategy: helm-set-image` in the Pipeline environment spec. kardinal
will update the `image.tag` (or a custom path) in `values.yaml` instead of Kustomize
overlays.

---

## Installation and Configuration

### What are the minimum cluster requirements?

- Kubernetes 1.28+
- The [krocodile Graph controller](installation.md#install-krocodile) installed
- A GitHub (or GitLab) personal access token with `repo` write scope

### What permissions does the controller need?

The Helm chart creates the necessary `ClusterRole`. The minimum permissions are:

- `get/list/watch/create/update/patch/delete` on all `kardinal.io` CRDs
- `get/list/watch/create/update/patch/delete` on `graphs.experimental.kro.run`
- `get/list/watch` on `deployments`, `pods`, `services`
- `get` on `secrets` (GitHub token secret only)
- `create/patch` on `events`
- `get/create/update` on `configmaps` (leader election)

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
standby and takes over if the primary crashes.

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
   kubectl logs -n kardinal-system deployment/kardinal-promoter-controller -f
   ```

### How do I manually approve a blocked bundle?

Use `kardinal approve` to patch the Bundle with an approval label, which bypasses
upstream gate requirements:

```bash
kardinal approve <bundle-name> --env prod
```

See [CLI Reference](cli-reference.md#kardinal-approve) for full options.

### How do I pause a promotion mid-flight?

```bash
kardinal pause <pipeline>
```

This prevents new Graphs from advancing. Existing in-flight PRs are not closed.
Resume with `kardinal resume <pipeline>`.

### What triggers a rollback?

Rollbacks are triggered:

1. **Manual**: `kardinal rollback <pipeline> --env prod` — opens a rollback PR
2. **Automatic**: If `spec.autoRollback.enabled: true` in a `RollbackPolicy` CRD and
   the health check fails after merge beyond the configured failure threshold

A rollback is a forward promotion of the previously-verified Bundle image through the
same pipeline, same gates, same audit trail.

### How do I see what changed between two promotions?

```bash
kardinal diff <pipeline> --env prod
```

Shows the diff between the current prod image and the pending promotion.

---

## Policy Gates

### Can I write a gate that allows hotfixes to bypass the weekend block?

Yes. Combine conditions:

```yaml
spec:
  expression: >
    !schedule.isWeekend() ||
    bundle.metadata.annotations.exists(a, a == 'kardinal.io/hotfix')
```

Annotate the Bundle at creation time to mark it as a hotfix.

### How often does kardinal re-evaluate a gate?

The `recheckInterval` field on the PolicyGate spec controls this (default: `5m`).
The PolicyGateReconciler re-runs the CEL expression on that schedule. When a gate
transitions from blocked to allowed, the Graph controller immediately advances.

### Can I test a gate without deploying?

```bash
kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"
# RESULT: BLOCKED
# no-weekend-deploys: !schedule.isWeekend() evaluated to false

kardinal policy test --file my-gate.yaml
# PASS: expression is valid CEL
```

---

## Architecture

### Why does kardinal need krocodile?

The Graph controller (krocodile) handles the complex part of DAG orchestration:
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
