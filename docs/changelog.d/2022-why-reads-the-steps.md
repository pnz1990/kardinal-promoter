---
section: Fixed
---

- **`kardinal why` no longer says a promoting environment has no version** (#2022) — since #1719, the Pipeline records a step's move between running states up to 2 seconds late. In that window, `kardinal why` (and the MCP `why` tool) could say "prod has no version yet" while prod's PR was already open, or "runs 1.4.0 and nothing is waiting" while 1.5.0 was promoting. why now projects each environment that has a step from the PromotionSteps and Bundles it reads, as the controller does, and keeps the recorded entry for an environment without a step.
