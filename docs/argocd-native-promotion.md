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
until the Application is Healthy and Synced and `status.summary.images` shows the version images.
Without it, the default `resource` adapter checks a Deployment.

### What the `argocd` strategy rejects

The `argocd` strategy patches the Application directly, so there is no pull request to review;
it sets only the image, so it cannot carry a config change; and it patches the one Application
`update.argocd.application` names, so it cannot promote a fleet, whose targets would all patch
that Application. kardinal rejects these cases before any Application is patched:

| Case | Where it is rejected | What you see |
|---|---|---|
| `approval: pr-review` with `update.strategy: argocd` | The API server, when you apply the Pipeline (CRD validation rule) | `kubectl apply` fails: `environments[]: update.strategy argocd patches the Application directly and cannot honour approval: pr-review; ...` |
| A `fleet` environment with `update.strategy: argocd` | The API server (CRD validation rule); the Pipeline's `Ready=False`, reason `ValidationFailed`, for a Pipeline stored before the rule | `kubectl apply` fails: `environments[]: update.strategy argocd cannot promote a fleet: every target would patch the fleet's one Application ...` Promote a fleet with a git-based strategy: each target writes its own path |
| The same, in a file | `kardinal validate -f pipeline.yaml` | `pipeline.yaml is invalid:` followed by the same message |
| A config or mixed version (`type: config` or `type: mixed`) when an environment it promotes uses `argocd` | Graph build, before the first environment | The version is `Failed`, with an `InvalidSpec` condition, reason `GraphBuildFailed` |

The `argocd-set-image` step checks both cases again as a last guard. It fails without patching
the Application if the approval is `pr-review` or the version is config or mixed.

The CRD rule applies only after the new CRDs are installed. Helm installs the chart's `crds/`
directory on first install and never upgrades it, so apply the new CRDs as described in
[Installation: Upgrade](upgrade.md). Until you do, the API server accepts the
combination, and the Pipeline reconciler, `kardinal validate` and the step still reject it.

For a gated promotion, use `approval: auto` with a gate to control when the patch happens.
For a reviewed promotion or a config version, use the `kustomize` or `helm` strategy. To send a
config version past an `argocd` environment, list that environment in `intent.skipEnvironments`.

---

## Required RBAC

The kardinal controller's ServiceAccount must have permission to `get`, `patch` and `update`
`applications.argoproj.io` in the namespace where your ArgoCD Applications live. The chart
grants `patch` by default, for the refresh request, but its `<fullname>-gitops-writes` admission
policy then lets the controller change only the `argocd.argoproj.io/refresh` annotation of an
Application it may not `update` ([Sync on promotion](health-adapters.md#sync-on-promotion)), so the
strategy's patch is denied. Grant `patch` and `update` on every Application with:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.2 \
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
    verbs: ["get", "list", "watch", "patch", "update"]
```

A Role like this one, with `resourceNames`, also grants `update` on only the Applications the
strategy patches, instead of `rbac.argocdApplicationsWrite` on every one. Without `update` the
step retries the denied patch and fails (`ValidatingAdmissionPolicy '<fullname>-gitops-writes' ...
denied request`).

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

When a version contains multiple images, the `argocd-set-image` step uses the **first image
with a tag**. An image pinned by digest is written as `<tag>@<digest>` (for example
`1.29.0@sha256:...`), as `helm-set-image` does, so a chart that renders
`image: {{ .Values.image.repository }}:{{ .Values.image.tag }}` pulls exactly that digest; this
is what [image verification](image-verification.md) needs, since a tag can be moved after it was
verified. An image with a digest but no tag is skipped (the key holds a tag); a version whose
images have no tag sets nothing: the step succeeds with `no image tag to set`. Setting different tags for different keys in one promotion is not
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
| Rollback mechanism | Forward promotion of an earlier version (commit or PR) | Forward promotion of an earlier version (Application patch) |
| Audit trail | Git history, PR and AuditEvent records | AuditEvent records only; no Git commit |
