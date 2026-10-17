---
section: Changed
---

- **`kardinal verify-receipt` prints each check on a line of its own** (#1951). Each line is `✓` when the check was made, `–` when it was not: the signature (with the key's fingerprint, or the certificate identity), the transparency log entry (`logIndex`), the timestamps, the statement, the subject digest, the resource pinned, the levels required, and the Bundle. The output never just says "verified". `-o json` adds the same list as `checks` ([Verify a receipt](receipts.md#verify-a-receipt))
