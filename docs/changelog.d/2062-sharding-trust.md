---
section: Documentation
---

- **What keeps tenants apart, and what does not** (#2062).
  - [Sharding](sharding.md#what-sharding-does-not-split): shards split the load, not trust. Each shard's controller can read the Secrets of every namespace, another shard's receipt signing key included, and the admission policies keep shards' writes apart only while no release lists another's controller.
  - [Receipts](receipts.md#where-receipts-are-stored): `receipts.sinks.oci.allowedRepositories` is controller-wide, so one tenant's receipt can be pushed as a referrer into another tenant's allowed repository. Run one release per tenant where that matters; a per-namespace list is planned (#2285).
  - [Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls): ChangeRequest providers are listed, and cluster and other private addresses are allowed by design. Narrow them with `egress.allowlist` or a NetworkPolicy.
