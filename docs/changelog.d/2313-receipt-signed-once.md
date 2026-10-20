---
section: Fixed
---

- **A receipt is signed once on a busy API server** (#2313). The controller decides each step of a receipt (sign, push the referrer, comment on the PR) on the API server's copy, not on its cache. Before, the reconcile right after a status write could read the cache from before that write. The receipt was then signed again: in keyless mode that meant a second Fulcio certificate and a second Rekor entry. In the scale suite this happened to 1,062 of 2,250 receipts, and 14.7% of reconciles failed on write conflicts. Only one signature was ever stored. ([Receipts](receipts.md#the-promotionreceipt))
