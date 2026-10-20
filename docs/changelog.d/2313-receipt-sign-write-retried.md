---
section: Fixed
---

- **The status write after a receipt's signature is retried before the receipt is signed again** (#2313). A write that fails with a server error, a timeout or throttling is now tried again for about three seconds on a fresh read, as a conflict already was. Before, the error ended the reconcile and the next one signed again: in keyless mode, a second Fulcio certificate and Rekor entry for one 500. A crash between the signature and its write still signs again. A refused write (a 403 or 422) is no longer retried with backoff, which signed again every few minutes: the receipt waits for its next change or a resync, with a Warning Event `StatusWriteRefused` and `kardinal_receipt_status_writes_refused_total{step}` ([Receipts](receipts.md#the-promotionreceipt))
