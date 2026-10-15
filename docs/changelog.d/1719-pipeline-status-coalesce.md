---
section: Changed
---

- **A large Pipeline writes its status less often** (#1719) — each state change of a PromotionStep made the Pipeline write its whole status again, and `status.environments` holds an entry per environment: a 496-environment wave wrote the Pipeline 921 times. A step moving between running states (promoting, waiting for its PR, checking health) now waits up to 2 seconds, and every such change of that window is written at once; a step that ends, a version's phase and a gate's result are written at once, as before. The same wave wrote the Pipeline 517 times, and a soak of 100 Pipelines about 2.9 status writes per Verified step instead of 5.7.
