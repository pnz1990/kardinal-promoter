---
section: Fixed
---

- **`kardinal init` no longer picks an Argo CD Application or Flux Kustomization that is being deleted** (v0.12 release gate). The Application might be one an ApplicationSet replaced under the same name, which would leave the environment checking a gone object. When some environments find one and others don't, init now names those that fall back to the Deployment health check, which most often means an Application not created yet. [Your first pipeline](first-pipeline.md) waits for the Applications before `kardinal init`.
