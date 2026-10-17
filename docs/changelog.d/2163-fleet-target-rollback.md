---
section: Changed
---

- **`kardinal rollback` says when a rollback goes through upstream environments** (#2163). A rollback whose images or chart are pinned by tag only is promoted through every environment upstream of its target first (a tag can be pushed again with other bytes, #2122), and there it supersedes a newer version in flight. `kardinal rollback` now says so when it creates one, and how to change the target alone: roll back to a version pinned by digest. The rollback of one fleet target pinned by digest already carries the verification of the environments before the fleet and changes only that target; the docs now say the fleet's other targets are not upstream of it ([Multi-Environment Rollback](rollback.md#multi-environment-rollback)).
