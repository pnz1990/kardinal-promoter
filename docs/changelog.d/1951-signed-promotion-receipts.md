---
section: Added
---

- **Signed promotion receipts** (#1951) — with `receipts.enabled`, every promotion that reaches Verified gets a PromotionReceipt: an in-toto SLSA verification summary whose `kardinalPromotion` record names the version, the actor and sponsor, the gates as they were when the step started, the approvals, the checks and the PR. The controller signs it (keyless through Fulcio, or a key in a Secret), keeps it in the cluster, and with `receipts.sinks` pushes it as an OCI referrer of each image and logs it in Rekor; it comments on the PR once. `kardinal verify-receipt` checks a receipt from the cluster, an image or a file, and fails on any change made after signing. `kardinal history` and `kardinal why` show the receipt. Off by default ([Signed promotion receipts](receipts.md)).
