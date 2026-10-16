---
section: Fixed
---

- **A PR merged before kardinal recorded it keeps its merge record** (#2065). If the controller stopped right after opening a `pr-review` PR, and the PR was merged before the restart, the restarted step found the change already on the base branch. It recorded `noChanges`, with no PR in its outputs and no `PRMerged` AuditEvent, so the review that released the environment was missing from `kardinal history`, `explain` and the audit trail. The step now looks up the merged PR of its own branch (only one opened after the step was created) and records the merge as for any PR: its number, URL, merge commit and the SCM-verified merger. A PR that is merged before kardinal first checks it also goes through that record now ([PR evidence](pr-evidence.md)).
- **open-pr adopts the open PR of its branch before opening one** — a re-run no longer relies on the SCM refusing a second PR. Bitbucket Cloud updates the existing PR instead of refusing, so a re-run used to overwrite its title and description ([SCM providers](scm-providers.md)).
