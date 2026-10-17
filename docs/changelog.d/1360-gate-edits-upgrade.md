---
section: Upgrade
---

- **Gate changes reach the versions in flight from the first gate change after the upgrade** (#1360) — no action is needed. A version that was promoting when you upgraded keeps the gates it has until one of its gates is created, edited or deleted; its gate copies get the `kardinal.io/gate-template-*` annotations then. If you relied on an in-flight version keeping an old gate, use `kardinal pause` or `kardinal hold` instead ([When a new or edited gate takes effect](policy-gates.md#when-a-new-or-edited-gate-takes-effect)).
