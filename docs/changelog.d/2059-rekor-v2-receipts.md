---
section: Added
---

- **Receipts logged in Rekor v2** (#2059) — `receipts.sinks.rekor.version: 2` logs each receipt signature in a Rekor v2 (rekor-tiles) log, such as the public `log20xx` shards or a private sigstore scaffolding stack, through the controller's egress guard; version 1 (`rekor.sigstore.dev`) stays the default ([Receipts](receipts.md#where-receipts-are-stored))
- **Keyless receipts with Rekor v2 need a timestamp authority** (#2059). A Rekor v2 entry does not prove the signing time, so the chart's values schema and the controller refuse keyless signing with `receipts.sinks.rekor.version: 2` and no `receipts.signing.tsaURL`. A receipt that could never verify is never marked Signed. Key and KMS receipts in Rekor v2 verify against a trusted root that holds the log, without one ([Receipts](receipts.md#where-receipts-are-stored))
