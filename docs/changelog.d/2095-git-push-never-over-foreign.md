---
section: Fixed
---

- **git-push never force-pushes over someone else's commit on kardinal's PR branch** (#2095). A step that pushed again, after a controller restart or a retry, overwrote any commit someone had pushed onto `kardinal/<hash>/<bundle>/<env>` in between, silently. git-push now reads the branch's head first and pushes, with a lease on that head, only when the branch is absent or its head is one of the commits the promotion pushed. Otherwise it pushes nothing, and the step fails against its retry budget with a `PRBranchForeignCommit` Warning Event and `status.outputs.prForeignHead`, as open-pr does. The PR branch's rebuild on a moved base uses the same check ([PR evidence](pr-evidence.md)).
