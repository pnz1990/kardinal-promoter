---
section: Development
---

- **The evidence pack at scale over versions, receipts and namespaces** (#2238) — `TestScale_EvidencePackVersions` exports 5,000 versions across 50 namespaces (500 across 10 in `ci`), each with an Approval, a gate instance and two signed receipts, cluster-wide, and fails when the CLI's peak RSS grows by more than 4 MiB over a period holding a tenth of the versions; `BenchmarkEvidencePackVersions` reports peak RSS and time at 1k, 5k and 10k versions.
