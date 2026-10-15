---
section: Documentation
---

- **The v0.9.0 upgrade test covers Graph retirement** (#1696) — a Bundle that finished under v0.9.0 has its Graph retired by the new controller: its step is kept in `status.retiredSteps`, `kardinal get bundles` and `history` still show it Verified, and a rollback to it deploys its image. The [upgrade guide](upgrade.md#upgrading-from-v090) says what happens to those Graphs.
