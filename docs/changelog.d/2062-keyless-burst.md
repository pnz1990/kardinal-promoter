---
section: Development
---

- **A receipt burst against a rate-limited Fulcio** (#2062) — the receipts-keyless suite's `TestReceiptsKeyless_Burst` promotes 100 versions at once while Fulcio answers 10 requests a second: promotions are not held up, receipts back off and are each signed once, and every signed receipt verifies. `hack/e2e/faultproxy` gains a `ratelimit` mode.
