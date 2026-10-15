---
section: Fixed
---

- **A step with no changes waited for the GitOps tool's poll** (#1910) — a step whose `git-commit` found nothing to commit waits for the branch head another writer pushed, but `health.refresh` never asked Argo CD or Flux to sync it, so the step waited minutes for the tool's poll. It now asks, once, when its health check finds the tool has not applied that head, and not when the tool already applied it ([Sync on promotion](health-adapters.md#sync-on-promotion))
