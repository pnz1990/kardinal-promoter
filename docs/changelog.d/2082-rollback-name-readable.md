---
section: Fixed
---

- **Shortened rollback names still say rollback** (#2082). An automatic rollback name that does not fit in 63 characters is now the start of the failing Bundle's name, then `-rollback-`, then a hash of the whole name. Before, the cut could drop `rollback` from the name. A name that fits is unchanged. A rollback created by an earlier version under another name is still found by its labels and reused.
