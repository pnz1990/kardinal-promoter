---
section: Added
---

- **The evidence pack holds the signed promotion receipts, and the page shows how to sign the pack** (#2052) — `kardinal audit evidence-pack` adds `receipts.ndjson`: each PromotionReceipt of the period's versions with its statement and Sigstore bundle exported as the controller signed them, so `kardinal verify-receipt --file` verifies it offline from the pack. A receipt whose signed statement holds a credential has its statement and bundle withheld and counted (`redactions.receiptsWithheld`). `<release>-auditor` reads `promotionreceipts`. [Produce an evidence pack](compliance.md#produce-an-evidence-pack) gives the `cosign sign-blob` and `verify-blob` commands that sign the pack under the exporter's identity, which CI runs in key mode.
