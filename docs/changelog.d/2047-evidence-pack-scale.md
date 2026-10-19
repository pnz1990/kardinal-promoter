---
section: Development
---

- **The evidence pack has a scale test and a benchmark** (#2047) — `TestScale_EvidencePack` exports 10,000 AuditEvents (100,000 in the `full` scale profile) and fails when the CLI's peak RSS grows by more than 4 MiB over exporting a namespace of a tenth as many (the pack must stream), passes 192 MiB, or takes more than 15 s (2 min at 100,000); it also checks that a second export is the same bytes. `BenchmarkEvidencePack` reports peak RSS and time at 10k, 50k and 100k events. AuditEvents only: Bundles, receipts and many namespaces are still to come.
