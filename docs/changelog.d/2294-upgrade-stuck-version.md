---
section: Documentation
---

- **A version stuck under #2271 recovers on upgrade** ([guide](upgrade.md#upgrading-from-v0120), #2294) — the `upgrade-v0120` and `upgrade-v0121` suites now upgrade with a version that v0.12.0 or v0.12.1 never created a verify gate for. With no step by hand, the new controller renders its Graph again, kro creates the gate, and the version promotes when the gate passes.
