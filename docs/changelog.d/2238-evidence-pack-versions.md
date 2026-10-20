---
section: Development
---

- **The evidence pack at scale over versions, receipts and namespaces** (#2238) — `TestScale_EvidencePackVersions` exports 5,000 versions across 50 namespaces (500 across 10 in `ci`), each with an Approval, a gate instance and two signed receipts, cluster-wide (5,000 versions: 6.8 s and a peak of 94 MiB on a kind cluster; a period of 500 of them, in the same cluster: 96 MiB), and fails when the CLI's peak RSS grows by more than 4 MiB over a period holding a tenth of the versions; `BenchmarkEvidencePackVersions` reports peak RSS and time at 1k, 5k and 10k versions. Both evidence pack scale tests now measure the CLI's own peak RSS (`/proc/<pid>/status` VmHWM, `scale.RunPeakRSS`): the rusage `Maxrss` they read before carried the test process's peak, so their memory checks never measured the CLI (#2320).
