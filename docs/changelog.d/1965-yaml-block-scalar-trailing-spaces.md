---
section: Fixed
---

- **kustomize-set-image and yaml-update keep a block scalar's trailing spaces** (#1965) — a folded or literal scalar whose last lines are spaces indented deeper than its text (`a: >` with a `"  "` line) was cut when the editor added an entry after it, and the file was then rewritten whole, the scalar re-encoded as a quoted string. Those lines are the scalar's text: only the edited entry's bytes change.
