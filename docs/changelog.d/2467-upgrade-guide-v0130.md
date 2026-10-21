---
section: Documentation
---

- **Upgrading from v0.13.0** ([guide](upgrade.md#upgrading-from-v0130), #2467) — the steps from v0.13.0 to this release: apply the CRDs, upgrade the chart, check the result with `kardinal doctor`, and roll back with `helm rollback` if you need to. The guide says what v0.13.0 stored keeps working. Receipts stay as v0.13.0 signed them and verify with the new CLI. Versions in flight keep their change requests, approvals, verdicts and overrides. The Graph of a version still promoting is rendered again, once, only when the release renders Graphs differently. After a rollback, v0.13.0 finishes the versions the new release started and verifies its receipts.
