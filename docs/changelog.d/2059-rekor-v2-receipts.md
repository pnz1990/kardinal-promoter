---
section: Added
---

- **Receipts logged in Rekor v2** (#2059) — `receipts.sinks.rekor.version: 2` logs each receipt signature in a Rekor v2 (rekor-tiles) log, such as the public `log20xx` shards or a private sigstore scaffolding stack, through the controller's egress guard; version 1 (`rekor.sigstore.dev`) stays the default ([Receipts](receipts.md#where-receipts-are-stored))
