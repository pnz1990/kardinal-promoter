---
section: Development
---

- **A receipt burst against a rate-limited Fulcio** (#2062) — the receipts-keyless suite's `TestReceiptsKeyless_Burst` gathers 100 receipts behind a refusing Fulcio, then lets it answer 1 request a second (configurable) with `Retry-After: 3`: promotions are not held up, receipts honour Retry-After without it cutting their backoff, never retry early, stay under a per-receipt attempt ceiling, are each signed once, and every signed receipt verifies. `hack/e2e/faultproxy` gains a `ratelimit` mode.
