---
section: Added
---

- **A `<release>-auditor` ClusterRole for evidence packs** (#2051) — `get` and `list` on exactly the kinds `kardinal audit evidence-pack` reads and on the chart's admission policies and their bindings, and no write; bind it with a ClusterRoleBinding. Bound in one namespace only, the pack lists the cluster-scoped kinds it cannot read (ChangeWindows, the admission policies; the agent directory is public) in `coverage.forbidden` ([User roles](guides/security.md#user-roles), [Produce an evidence pack](compliance.md#produce-an-evidence-pack)).
