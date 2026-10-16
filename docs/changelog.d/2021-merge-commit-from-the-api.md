---
section: Security
---

- **The merge commit comes from the SCM API, not the webhook payload** (#2021) — the merged-PR webhook recorded `PRStatus.status.mergeCommitSHA` from its payload, which anyone holding the webhook secret could write (GitLab and Azure DevOps send it in plain text); that commit is the one the `argocd` and `flux` health checks require deployed. The webhook now only marks the PR merged, and the PRStatus reconciler asks the SCM API for the commit right after, retried for its 5-minute window, as it already did for merges found by polling. The payload's commit is logged as a hint ([When the merge commit is not known yet](health-adapters.md#when-the-merge-commit-is-not-known-yet)).
