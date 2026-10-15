---
section: Fixed
---

- **The free text a caller sends is bounded** (#1867) — `spec.provenance.author` (256 characters), `spec.provenance.ciRunURL` (2048), a gate override's `reason` (1024) and `stage` (63), and a ChangeWindow's `reason` (1024) now have limits, as rejection reasons and approval comments already did. Before, a 70 KB author made the default PR body longer than GitHub's 65,536-character limit, so the version could not promote anywhere a PR is opened. The author could come from an agent through `kardinal mcp`'s ship tool, or from `kardinal create bundle --author`. An override reason went into gate reasons and PR bodies the same way.
