---
section: Fixed
---

- **kustomize-set-image, helm-set-image and yaml-update leave the rest of the file as it was** (#1965). Fuzzing found these cases:
  - A block scalar's trailing spaces (`a: >` followed by a line of spaces deeper than its text) were cut when an entry was added after it.
  - A quoted scalar over several lines lost a line of it that starts with `#`.
  - An entry was added after a `...` document end marker.
  - Blank lines with tabs were dropped.
  - An indentation indicator on a header on its own line was misread.

  Each made the editor rewrite the whole file. Only the edited entry's bytes change now. When a file must still be rewritten whole, the editor writes what it would write again on the next run, so the same edit always writes the same bytes. A value that would read back as another tag (`<<`) is quoted.
