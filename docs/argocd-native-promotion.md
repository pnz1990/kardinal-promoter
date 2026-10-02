# ArgoCD-Native Promotion

kardinal supports a **direct ArgoCD Application patch** promotion path (`update.strategy: argocd`)
for teams that store application configuration inside ArgoCD Applications rather than a GitOps repo.

This is the Kargo `argocd-update` equivalent: no git commit, no PR, no branch required.
The controller patches `spec.source.helm.valuesObject` on the ArgoCD `Application` resource
directly via the Kubernetes API.

---

## When to use this

Use `update.strategy: argocd` when:

- Your ArgoCD Applications use **inline Helm values** (`spec.source.helm.valuesObject`)
  rather than a committed `values.yaml` in a GitOps repo.
- You want promotions to take effect immediately (no PR merge delay).
- Your image references live in the ArgoCD Application spec, not in a Kustomize overlay.

Use the default `kustomize` or `helm` strategy when your environments are managed through
a GitOps repo with environment-specific overlays.

---

## Pipeline configuration

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  # Required by the CRD even though the argocd strategy makes no git commits.
  git:
    url: https://github.com/myorg/my-app-gitops
    secretRef:
      name: github-token
  environments:
    - name: test
      approval: auto
      update:
        strategy: argocd
        argocd:
          application: my-app-test   # ArgoCD Application name
          namespace: argocd           # namespace where the Application lives (default: "argocd")
          imageKey: image.tag         # dot-path within valuesObject (default: "image.tag")
      health:
        type: argocd

    - name: prod
      approval: auto
      update:
        strategy: argocd
        argocd:
          application: my-app-prod
          namespace: argocd
          imageKey: image.tag
      health:
        type: argocd
```

---

## What the step does

When `update.strategy: argocd` is used, the promotion sequence is:

```
argocd-set-image → health-check
```

There are no git operations. The `argocd-set-image` step:

1. Reads the current ArgoCD `Application` to check whether the target image tag is already set (idempotency).
2. If already set: returns success immediately (no-op).
3. If not set: applies a JSON merge patch to `spec.source.helm.valuesObject.<imageKey>`.

After the patch, ArgoCD's own reconciler picks up the spec change and syncs the application.
The `health-check` step runs the environment's health adapter. Set `health.type: argocd` to wait
until the Application is Healthy and Synced and `status.summary.images` shows the Bundle images.
Without it, the default `resource` adapter checks a Deployment.

### What the `argocd` strategy rejects

The `argocd` strategy patches the Application directly, so there is no pull request to review,
and it sets only the image, so it cannot carry a config change. kardinal rejects both cases
before any Application is patched:

| Case | Where it is rejected | What you see |
|---|---|---|
| `approval: pr-review` with `update.strategy: argocd` | The API server, when you apply the Pipeline (CRD validation rule) | `kubectl apply` fails: `environments[]: update.strategy argocd patches the Application directly and cannot honour approval: pr-review; ...` |
| The same, on a Pipeline stored before the rule existed | The Pipeline reconciler | The Pipeline's `Ready` condition is `False` with reason `ValidationFailed` and the same message |
| The same, in a file | `kardinal validate -f pipeline.yaml` | `✗ pipeline.yaml is invalid:` followed by the same message |
| A config or mixed Bundle (`type: config` or `type: mixed`) when an environment it promotes uses `argocd` | Graph build, before the first environment | The Bundle is `Failed`, with an `InvalidSpec` condition, reason `GraphBuildFailed` |

The `argocd-set-image` step checks both cases again as a last guard. It fails without patching
the Application if the approval is `pr-review` or the Bundle is config or mixed.

The CRD rule applies only after the new CRDs are installed. Helm installs the chart's `crds/`
directory on first install and never upgrades it, so apply the new CRDs as described in
[Installation: Upgrade](installation.md#upgrade). Until you do, the API server accepts the
combination, and the Pipeline reconciler, `kardinal validate` and the step still reject it.

For a gated promotion, use `approval: auto` with a PolicyGate to control when the patch happens.
For a reviewed promotion or a config Bundle, use the `kustomize` or `helm` strategy. To send a
config Bundle past an `argocd` environment, list that environment in `intent.skipEnvironments`.

---

## Required RBAC

The kardinal controller's ServiceAccount must have permission to `get` and `patch`
`applications.argoproj.io` in the namespace where your ArgoCD Applications live. The chart
grants read access by default; enable `patch` with:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system --reset-then-reuse-values \
  --set rbac.argocdApplicationsWrite=true
```

In namespace-scoped mode (`controller.watchNamespace`) the chart's rules apply only to the
watched namespace. If your Applications live elsewhere (for example `argocd`), create a Role
there and bind it to the controller's ServiceAccount:

```yaml
rules:
  - apiGroups: ["argoproj.io"]
    resources: ["applications"]
    verbs: ["get", "list", "watch", "patch"]
```

---

## imageKey dot-path

`imageKey` is a dot-separated path within `spec.source.helm.valuesObject`.

| `imageKey` | Patches |
|---|---|
| `image.tag` (default) | `spec.source.helm.valuesObject.image.tag` |
| `app.version` | `spec.source.helm.valuesObject.app.version` |
| `myService.image.tag` | `spec.source.helm.valuesObject.myService.image.tag` |

The step creates intermediate maps as needed if they do not exist.

---

## Multi-image bundles

When a Bundle contains multiple images, the `argocd-set-image` step uses the **first image
with a non-empty tag**. A Bundle whose images have only digests sets nothing: the step succeeds
with `no image tag to set`. Setting different tags for different keys in one promotion is not
supported. kardinal has no custom step sequence to do it (a Pipeline that sets
`spec.environments[].steps` is rejected; see [Promotion Steps](pipeline-reference.md#promotion-steps)).

---

## Comparison with git-write strategies

| Feature | `kustomize` / `helm` | `argocd` |
|---|---|---|
| Requires GitOps repo | Yes | No |
| Creates a git commit | Yes | No |
| Opens a PR | Yes (pr-review mode) | No (`approval: pr-review` is rejected) |
| Promotion speed | PR merge required | Immediate |
| Rollback mechanism | Forward promotion of an earlier Bundle (commit or PR) | Forward promotion of an earlier Bundle (Application patch) |
| Audit trail | Git history, PR and AuditEvent records | AuditEvent records only; no Git commit |
