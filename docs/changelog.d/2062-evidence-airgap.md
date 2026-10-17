---
section: Development
---

- **The evidence-airgap live suite** (#2062) — receipts, attestations and the evidence pack with no way out of the cluster: the controller's namespace reaches only the cluster and the API server. A version's key signature and its keyless provenance from a private Sigstore (`trustedRootRef`) verify from the in-cluster registry, its receipts are signed and stored there, `kardinal verify-receipt` passes in a network namespace with only a loopback, and the pack's checksums and receipts verify offline. A keyless authority that needs the public-good root fails closed.
