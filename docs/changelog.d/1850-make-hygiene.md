---
section: Development
---

- **`make hygiene`**, and a `hygiene` CI job, fail a PR that adds dead code or dead files: a Go function no main or test reaches (deadcode), a docs page neither in the nav nor linked, a docs asset or a script nothing names, and an unused file, export or dependency in the web UI (knip). Exceptions go in `hack/hygiene/allow.txt`, each with its reason. The test-only helpers left in production code moved into `export_test.go` files, and the dead functions are removed.
