---
section: Upgrade
---

- **kardinal's PR branch is kardinal's** (#2079, #2095) — a PR create the SCM refuses counts as a duplicate only on the provider's own answer, an open PR of kardinal's branch is adopted only when its head is, or contains, the commit kardinal pushed, and git-push no longer force-pushes over a commit someone else pushed to kardinal's PR branch. Where v0.12.0 adopted such a PR or overwrote the commit, the step now fails against its retry budget with a `PRBranchForeignCommit` Warning Event that names the foreign head; any other refused create fails with the SCM's own message. This applies to steps already in flight when you upgrade: kardinal still recognises their own pushes (v0.12.0 recorded them), but one that pushes again, or opens its PR again, over a commit someone else pushed to its branch now fails instead of overwriting the commit or adopting the PR ([PR evidence](pr-evidence.md), [SCM providers](scm-providers.md#supported-providers)).
