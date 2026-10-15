---
section: Upgrade
---

- **Pipelines that deploy outside their namespace no longer refresh Argo CD or Flux** (#1781) — when your Pipelines are in one namespace and their Applications or Kustomizations deploy to others (for example a Pipeline in `default` and Applications deploying to `my-app-prod`), their steps now wait for the tool's poll, with `refresh skipped (...)` in `status.refreshMessage`. Admit them with `health.refresh.namespaces`, for example `["default=my-app-*"]`, or `["*=*"]` for the behaviour before v0.12 ([Sync on promotion](health-adapters.md#sync-on-promotion))
