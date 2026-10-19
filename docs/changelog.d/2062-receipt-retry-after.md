---
section: Changed
---

- **Receipts honour Retry-After, and their retries are spread** (#2062) — when Fulcio, Rekor or the timestamp authority answers 429 or 503 with `Retry-After`, a receipt is signed again after that wait (at most 5 minutes) instead of the 30-second-doubling backoff. Every retry wait gets a random part of up to its own length, so a burst of receipts does not retry in step. `status.lastAttemptAt` records when the last failed attempt ran ([Signed promotion receipts](receipts.md)).
