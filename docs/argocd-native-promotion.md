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
The `health-check` step waits for the ArgoCD Application to reach a healthy sync state.

---

## Required RBAC

The kardinal controller's ServiceAccount must have permission to `get` and `patch`
`applications.argoproj.io` in the namespace where your ArgoCD Applications live.

Add to your Helm values or RBAC manifest:

```yaml
# Example: ClusterRole extension or Role in the argocd namespace
rules:
  - apiGroups: ["argoproj.io"]
    resources: ["applications"]
    verbs: ["get", "patch"]
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
with a non-empty tag**. For multi-image promotions where different keys need different
tags, use custom steps via `PromotionStep.Spec.Inputs`:

```yaml
# PromotionStep override (advanced)
spec:
  steps:
    - name: argocd-set-image
      inputs:
        argocd.application: my-app-prod
        argocd.namespace: argocd
        argocd.imageKey: frontend.tag
```

---

## Comparison with git-write strategies

| Feature | `kustomize` / `helm` | `argocd` |
|---|---|---|
| Requires GitOps repo | Yes | No |
| Creates a git commit | Yes | No |
| Opens a PR | Yes (pr-review mode) | No |
| Promotion speed | PR merge required | Immediate |
| Rollback mechanism | Git revert PR | Re-promote previous bundle |
| Audit trail | Git history + PR | Kubernetes event log |
