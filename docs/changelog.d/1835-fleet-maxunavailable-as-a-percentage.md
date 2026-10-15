---
section: Added
---

- **`fleet.maxUnavailable` as a percentage** (#1835) — `maxUnavailable: "10%"` stops a fleet's rollout once that share of its targets failed, rounded up and at least 1 (`"10%"` of 50 targets is 5). The API server refuses a count below 1 and a percentage outside 1% to 100%. A new CI job (`make envtest`) installs every CRD and the chart's admission policies on the oldest supported and the newest API server. The UI API's fleet carries the count and `maxUnavailableSpec`, the value as written. See [Fleets](pipeline-reference.md#fleets)
