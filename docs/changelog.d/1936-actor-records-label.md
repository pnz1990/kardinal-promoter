---
section: Changed
---

- **The controller's Pipeline and Bundle writes no longer pay for the agent registry** (#1936) — `<release>-actor-records` converted the whole AgentIndex for every Pipeline create and update and every Bundle update, the controller's included: about 16 ms at 2,000 agents and 32 ms at 4,200 per write. It now matches only Pipelines and Bundles labelled `kardinal.io/actor-check`, which a write that records who paused, resumed, held, released or rejected carries (`kardinal` and the UI set it; the new params-free `<release>-actor-record-marker` requires it). A kubectl or script write that sets `spec.holds`, `spec.lastChange` or `spec.rejected` must add the label `kardinal.io/actor-check: "true"` in the same write ([Agents and the verified actor](actors.md))
