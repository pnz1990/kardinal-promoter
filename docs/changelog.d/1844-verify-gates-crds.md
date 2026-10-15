---
section: Upgrade
---

- **Verify gates add three CRDs** (#1844) — apply `verdicts`, `agentruns` and `steptemplates` with the other CRDs before the chart; `policygates` gains `spec.verify`, `spec.verdicts` and `status.verdicts`. Existing gates are unchanged, and nothing runs until a gate sets `spec.verify` ([Verify with agents](agentic/verify-with-agents.md))
