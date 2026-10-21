---
section: Added
---

- **What a shared branch's writers cost each other is counted** (#2366). A git-push step on an `approval: auto` environment keeps its totals for its whole life, across fresh clones and reconciles: `status.outputs.pushRefusals` (pushes refused because the branch moved), `status.outputs.rebasesTotal` (rebases made) and `status.outputs.pushRestarts` (fresh clones it asked for). `kardinal_git_operations_total` and `kardinal_git_operation_duration_seconds` now count and time rebases too (`operation="rebase"`, with the results `ok`, `conflict`, `base_missing`, `not_moved` and `error`).
