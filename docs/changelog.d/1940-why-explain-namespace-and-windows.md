---
section: Fixed
---

- **`why` and `explain` print commands that run as is, and say when a ChangeWindow gate opens** (#1940) — every command they print names `-n <namespace>` when the pipeline's namespace is not the kubeconfig context's, as `kardinal inbox` does. A gate whose expression is one `changewindow.isAllowed("…")` or `!changewindow.isBlocked("…")` says when it opens, to the minute (`by itself at Thursday 04:25 UTC, in about 18m`). `kardinal policy simulate` finds the next window at the minute a ChangeWindow changes, not only at whole hours, so a window opening at 04:25 reads 04:25 instead of 05:00. `why` for an environment with a newer version still upstream says it is on its way and where, instead of "nothing is waiting".
