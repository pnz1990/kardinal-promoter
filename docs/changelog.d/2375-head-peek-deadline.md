---
section: Fixed
---

- **The PR-branch same-content check has a deadline** (#2375). A git server that stalls part-way through its response, its stream left open, no longer holds a promotion step's worker. The peek of the branch's head now ends after 2 minutes, or sooner if the step's own deadline is sooner, with an error saying the server did not send the head's commit in time, and the step retries it like any fetch error.
