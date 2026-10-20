---
section: Changed
---

- **Receipts honour Retry-After, and their retries are spread** (#2062) — when Fulcio, Rekor or the timestamp authority answers 429 or 503 with `Retry-After`, a receipt waits the longer of that wait (at most 5 minutes) and its 30-second-doubling backoff, so a short `Retry-After` never makes it retry faster. Every retry wait gets a random part of up to its own length (a wait is between the backoff and twice it, at most two hours), so a burst of receipts does not retry in step. `status.lastAttemptAt` records when the last failed attempt ran ([Signed promotion receipts](receipts.md)).
