---
section: Development
---

- **Journeys 1 and 2 go through the Graph** (#1291) — the fake-client journey tests built their PromotionSteps by hand. They now build the Bundle's Graph with the translator and create its steps along the Graph's edges, checking the edges and step templates (kro's gating runs in the live suites): Journey 1 sets the Bundle's image in each environment's kustomization, test then uat then prod, and Journey 2 fans out from pre-prod to prod-eu and prod-us. Journey 3 checks the business-hours gate its comment names. On kind, the quickstart example test checks that the Graph creates uat's step only once test is Verified.
