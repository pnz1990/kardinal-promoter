---
section: Fixed
---

- **A merged PR with a reviewer's fixup on top is adopted after a restart** (#2092). When the controller restarted after opening a PR, and a reviewer pushed a fixup to it and merged it, the step recorded "no changes" and no `PRMerged` AuditEvent, because only a PR whose head was kardinal's commit counted. kardinal now also adopts the merged PR of its own branch whose commits, as the SCM lists them, include a commit the step pushed. This works on GitHub, GitLab, Forgejo, Gitea, Bitbucket Cloud and Data Center, and Azure DevOps, after squash and rebase merges, and once the branch is deleted. A list longer than one page is not used ([PR evidence](pr-evidence.md)).
