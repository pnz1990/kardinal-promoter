---
section: Fixed
---

- **`layout: branch` pull requests get the `kardinal/gates` check** (#1854) — the render step recorded the PR branch but not the commit the render Job pushed to it, so the gate results were never posted on a rendered environment's PR, and a branch protection rule that requires the check blocked it.
