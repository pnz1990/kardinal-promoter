---
section: Fixed
---

- **The PR-branch same-content check no longer waits for ever on a local repository** (#2364). With a `file://` or path remote, the check that a head of kardinal's PR branch holds the same content stopped reading once it had the commit, but could leave the local `git-upload-pack` running, blocked writing the rest of the pack, and then waited for it to exit: a promotion step hung. It now reads until the cancelled transport fails, which kills the process. Remotes over HTTPS and ssh were not affected.
