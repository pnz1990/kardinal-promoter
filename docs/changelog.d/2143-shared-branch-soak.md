---
section: Development
---

- **The shared-branch writers test explains a lost write, and soaks nightly** (#2143) — when a writer's file is missing, `TestSharedBranch_SixtyWritersThroughTheEngine` now logs main's history and the commit that dropped it, with that commit's changes, tells a missing file from an object it could not read (go-git reports both as "file not found"), and reads the branch once more on a failed read. A nightly workflow, `shared-branch-soak.yml`, runs it 200 times under load and keeps the logs of a failure.
