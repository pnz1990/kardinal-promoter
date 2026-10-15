---
section: Fixed
---

- **A step is labelled with the step that writes its change** (#1911) — a PromotionStep's `spec.stepType`, which `kardinal explain`, `kardinal get steps` and the UI print, followed the Bundle type only, so an image Bundle on an `update.strategy: yaml`, `helm` or `argocd` environment, or one that renders to a branch, read `kustomize-set-image`. It is now `yaml-update`, `helm-set-image`, `argocd-set-image` or `render`, as the step runs. Steps created before the upgrade keep their label.
