---
section: Added
---

- **Structured approval fields** (#2057, [Approval fields](approvals.md#approval-fields)). An approval gate can declare what an approve must carry: a change ticket that matches a pattern, an acknowledgement that must be `true`, a URL, an integer, or one of a list of values. The values are given with `kardinal approve --field name=value`, in the UI's approval form, or with the MCP `approve` tool's `fields`.
  - **Checked before anything is written.** The CLI and the UI refuse an approve that does not satisfy the gate. At a terminal, `kardinal approve` asks for a missing required field. An Approval written another way is admitted if it is well formed, but does not count, and the gate says which field is missing.
  - **In the gate's expression.** `approvals.fields.<name>` holds the values the counted approvals gave.
  - **In the records.** The fields appear in `status.approvals` and in the `ApprovalRecorded` AuditEvent.
  - **In `kardinal why`.** It lists the fields a waiting gate asks for.
