---
section: Fixed
---

- **UI approvals for approvers who can't list PolicyGates** (#2267). Since structured approval fields (#2119), the UI read the gates' `approval.fields` with the approver's own rights before recording an approval. A role that can create Approvals but not list PolicyGates, as the [RBAC requirements](guides/security.md#rbac-requirements) allow, got `403 forbidden: ... cannot list policygates`, and nothing was recorded. The UI now checks first whether the user may list the gates, without counting a "no" as a denial. If they can't, it records the approval and leaves the field rules to the gate's tally, as `kardinal approve` already did. Users without `create` on approvals are still refused.
