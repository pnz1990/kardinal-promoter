---
section: Development
---

- **The evidence pack has a scale test and a benchmark** (#2047) — `TestScale_EvidencePack` exports 10,000 AuditEvents (100,000 in the `full` scale profile) and holds the CLI to a peak RSS of 192 MiB and a time budget, and checks a second export is the same bytes; `BenchmarkEvidencePack` reports peak RSS and time at 10k, 50k and 100k events.
