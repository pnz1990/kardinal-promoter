---
section: Fixed
---

- **Previews of a Pipeline with `verification`** (#2011). `kardinal policy simulate`, `kardinal policy list --pipeline`, `kardinal validate` and `kardinal create bundle --dry-run` failed with `AnalysisTemplate "<name>" not found` for any Pipeline whose environments use `spec.verification`, although the controller promotes it. They now preview it without reading its AnalysisTemplates: verification adds no gate, so they show the controller's gates and order; the dry run names each environment's analyses ([Previews](analysis.md#previews)).
