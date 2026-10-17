---
section: Added
---

- **`<release>-auditor` and `<release>-auditor-cluster` ClusterRoles for evidence packs** (#2051) — together they grant `get` and `list` on exactly what `kardinal audit evidence-pack` reads, and no write. `-auditor` covers the namespaced records; bind it with a RoleBinding in each namespace an auditor may read (or a ClusterRoleBinding for all). `-auditor-cluster` covers ChangeWindows, AgentIdentities and the chart's admission policies, always with a ClusterRoleBinding. A namespace auditor then gets a full pack of its namespace and no other namespace's records ([User roles](guides/security.md#user-roles), [Produce an evidence pack](compliance.md#produce-an-evidence-pack)).
