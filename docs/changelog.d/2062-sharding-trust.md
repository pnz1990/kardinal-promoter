---
section: Documentation
---

- **Shards split the load, not trust** (#2062) — [Sharding](sharding.md#what-sharding-does-not-split) now says that each shard's controller can read the Secrets of every namespace, another shard's receipt signing key included, and that the admission policies keep shards' writes apart only while no release lists the other's controller. To keep teams apart, run one release per team in namespace mode.
