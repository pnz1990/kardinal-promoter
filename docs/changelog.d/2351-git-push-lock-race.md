---
section: Fixed
---

- **A git-push on a shared branch no longer fails when its push loses a race to another writer's update still in flight** (#2351). A server refuses such a push with "cannot lock ref" or "failed to lock" while the other update holds the branch's lock. The fetch that follows could still see the branch at the commit the change was made on, so the step failed with "refused as non-fast-forward, but the remote branch did not move". git-push now fetches the branch again, backing off for up to 2 seconds, and rebases once it moves. A branch that hasn't moved by then is still reported as stuck, and the step's message now gives the server's reason, such as `cannot lock ref 'refs/heads/main': Unable to create '…/main.lock': File exists` for a stale lock file.
