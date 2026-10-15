---
section: Fixed
---

- **A step waiting on an older commit did not say so, and a remote cluster's refresh permission was only found at promotion time** (#1883) — when Argo CD or Flux applied an older commit of the branch than the promoted one (after a refused refresh its poll can keep that head for minutes), the step's message now says how far behind it is (`dc6706827cb7 is 1 commit behind it: Argo CD has not fetched the promoted commit yet`). `kardinal doctor --pipeline` now checks, for each environment with `health.kubeconfigSecretRef`, that the kubeconfig's identity may `get` the object its health check names (FAIL) and `patch` it for `health.refresh` (WARN), with a SelfSubjectAccessReview on the remote cluster ([Remote Clusters](health-adapters.md#remote-clusters))
