---
section: Added
---

- **`kardinal audit evidence-pack`: the records of a period in one archive for an auditor** (#2050) — one gzipped tar with the period's AuditEvents (JSON lines and OCSF), the versions created in it with their Approvals, gate instances and Verdicts, the Pipelines, gate templates, ChangeWindows, AgentIdentities and admission policies in force, `controls.json` (which record evidences which control) and a manifest with each file's SHA-256, plus `SHA256SUMS` for `sha256sum -c`. `coverage` in the manifest gives the oldest record of each kind the cluster still holds and the kinds it does not serve or you may not read; a period that retention already deleted fails with the advice to export continuously. The pack is written beside `-o` and renamed when complete, read page by page (500 objects), and the same records give the same bytes ([Produce an evidence pack](compliance.md#produce-an-evidence-pack)).
