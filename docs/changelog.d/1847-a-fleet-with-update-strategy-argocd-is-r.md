---
section: Fixed
---

- **A fleet with `update.strategy: argocd` is refused** (#1847) — every target inherited the fleet's `update.argocd.application`, so each target's promotion patched that one Application while its own health check read another. The Pipeline CRD refuses the combination, and the Pipeline's `Ready=False/ValidationFailed` and `kardinal validate` report a Pipeline stored before the rule. Promote a fleet with a git-based strategy ([Argo CD native promotion](argocd-native-promotion.md#what-the-argocd-strategy-rejects)).
