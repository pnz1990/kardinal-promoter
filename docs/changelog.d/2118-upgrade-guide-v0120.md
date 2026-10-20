---
section: Documentation
---

- **Upgrading from v0.12.0** ([guide](upgrade.md#upgrading-from-v0120), #2118) — the steps from v0.12.0 to v0.13.0, with the four changes to read first: `graph.readerNamespaces` is removed (check `helm get values`, and upgrade with a values file without it if you set it), a rollback changes only its target environment when its artifacts are pinned by digest and were verified upstream, gate changes reach the versions in flight, and `provenance.ciRunURL` refuses credential parameters. It names the new CRDs, the receipts admission policy and the auditor roles, and kro stays at the same version. The downgrade notes and the v0.8.1 guide's note on reader bindings now say which release each applies to.
