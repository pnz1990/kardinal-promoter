---
section: Added
---

- **Verdicts in the UI** (#1908) — a verify gate's verdicts in the stage drawer's **Why it's waiting** (the passing verdicts still missing, with the quorum) and on **Activity**: each verdict names its verifier with the actor badge, says its result in words, counts its high findings and links its evidence; an inconclusive verdict says whether the gate holds, retries or counts it as a fail. The UI API's verdicts carry `verifierActor` and `highFindings`, and its actors `principal`. See [Verify gates](ui.md#verify-gates).
