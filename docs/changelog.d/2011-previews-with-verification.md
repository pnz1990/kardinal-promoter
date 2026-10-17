---
section: Fixed
---

- **Previews of a Pipeline with `verification`** (#2011). `kardinal policy simulate`, `kardinal policy list --pipeline`, `kardinal validate` and `kardinal create bundle --dry-run` failed with `AnalysisTemplate "<name>" not found` for any Pipeline whose environments use `spec.environments[].verification`, even when the template exists. They now read the AnalysisTemplates as the controller does. A template that does not exist, or Argo Rollouts not installed, makes `policy simulate` print `RESULT: BLOCKED` by verification and exit non-zero, as the controller fails such a version. When the CLI may not read the templates, the output says `Verification not checked` and why. `validate` stays offline and checks the rest ([Previews](analysis.md#previews)).
