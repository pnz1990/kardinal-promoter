---
section: Fixed
---

- **The receipt of a version with 15 or more images reaches Stored with the OCI sink** (#2331). A receipt's `status.stored` held at most 16 entries: the CR, Rekor, and one per image's referrer. So the status write after the referrers of 15 images or more was refused every time, the receipt stayed Signed, and its referrers were pushed again at each retry. It now holds 102, enough for the 100 images a Bundle may have. ([Where receipts are stored](receipts.md#where-receipts-are-stored))
