# Spec: argocd-set-image step (issue #915)

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 1: Kargo parity`
- **Implements**: No ArgoCD-native image update step (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: A new built-in step named `argocd-set-image` is registered in the step registry
via `init()` in `pkg/steps/steps/argocd_set_image.go`. It is discoverable via
`steps.Lookup("argocd-set-image")` and appears in `steps.Registered()`.

**O2**: The step patches the ArgoCD `Application` resource at
`spec.source.helm.valuesObject.<imageKey>` using `state.K8sClient`. The Application
name is read from `state.Inputs["argocd.application"]`. The namespace is read from
`state.Inputs["argocd.namespace"]` (defaults to `"argocd"`). The YAML key path within
`valuesObject` is read from `state.Inputs["argocd.imageKey"]` (defaults to `"image.tag"`).

**O3**: The step is idempotent. Patching the same image tag twice produces the same
`Application.spec` state. The step returns `StepSuccess` if the Application already
has the target tag.

**O4**: When `state.K8sClient` is nil, the step returns `StepFailed` with a message
containing "K8sClient is required".

**O5**: When `state.Inputs["argocd.application"]` is empty, the step returns `StepFailed`
with a message containing "argocd.application input is required".

**O6**: When the target Application is not found, the step returns `StepFailed` with
a message containing "not found".

**O7**: The step does NOT require git operations. It does NOT call `state.GitClient`,
`state.SCM`, or access `state.WorkDir`. The entire promotion is a Kubernetes patch only.

**O8**: The `UpdateConfig` API type gains a new `ArgoCD *ArgoCDUpdateConfig` field with
`json:"argocd,omitempty"`. `ArgoCDUpdateConfig` has fields: `Application string`,
`Namespace string`, `ImageKey string`. The `UpdateConfig.Strategy` enum gains `"argocd"`
as a valid value. The `DefaultSequenceForBundle` function produces `["argocd-set-image", "health-check"]`
when `updateStrategy == "argocd"`.

**O9**: A unit test `TestArgoCDSetImageStep_*` covers: success case (Application patched),
idempotency (second run = StepSuccess), missing application name (StepFailed), nil K8sClient (StepFailed).

**O10**: `docs/design/15-production-readiness.md` moves the `argocd-set-image` item
from 🔲 Future to ✅ Present.

**O11**: `docs/argocd-native-promotion.md` is created with user-facing documentation
explaining the `argocd-set-image` step, showing an example Pipeline spec using
`update.strategy: argocd`, `update.argocd.application`, `update.argocd.imageKey`.

---

## Zone 2 — Implementer's judgment

- The patch strategy: use a JSON merge patch on `Application.spec.source.helm.valuesObject`.
  The `valuesObject` is a free-form map — we set a nested key path within it.
- If `spec.source.helm` is nil, create it. If `valuesObject` is nil, create it.
- ArgoCD Application type: use `unstructured.Unstructured` with GroupVersionResource
  `argoproj.io/v1alpha1/applications` to avoid importing the ArgoCD SDK (keeps go.mod lean).
- The step skips git-clone, kustomize, git-commit, git-push, open-pr, wait-for-merge entirely.
  The pipeline uses a shorter sequence: `["argocd-set-image", "health-check"]`.

---

## Zone 3 — Scoped out

- Multi-source Applications (`spec.sources[]`) — only `spec.source` is supported.
- Non-Helm Applications (Kustomize, plain YAML, raw helm) — only `spec.source.helm.valuesObject` is targeted.
- ArgoCD refresh / sync triggering after the patch — the ArgoCD controller will pick up
  the spec change automatically via its reconcile loop.
- RBAC validation for ArgoCD Application access — the controller's ServiceAccount must have
  `patch` on `applications.argoproj.io`. This is documented in the user doc but not enforced by the step.
