---
section: Added
---

- **The merge of a pr-review PR is audited** (#1939) — a new `PRMerged` AuditEvent per promotion step names who merged its pull request, asked of the SCM API once the merge is confirmed (GitHub, GitLab, Forgejo, Gitea, Bitbucket Cloud and Data Center, Azure DevOps; never taken from a webhook payload); the step waits for the name for at most 5 minutes, whether the webhook or polling saw the merge, and the merge commit. PRStatus records `status.mergedBy` and `status.mergedVia`, and the step's `status.outputs.prMergedBy` names the merger. With `approval: pr-review` as an environment's only human gate, the audit trail now names the person who released to it ([Audit logging](guides/security.md#audit-logging)).
