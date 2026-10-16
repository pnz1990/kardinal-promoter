---
section: Documentation
---

- **Upgrading from v0.11.0** ([guide](upgrade.md#upgrading-from-v0110)) — the steps from v0.11.0 to the next release, run as written by a new live suite, `upgrade-v0110`, on Kubernetes 1.35 and 1.37. It upgrades an estate of 30 Pipelines in 5 namespaces: versions in every phase, open pr-review PRs, approvals given and pending, a fleet, a hold and a paused Pipeline. In-flight versions keep their Graphs, steps and PRs and finish, approvals still count, and holds and pauses stay.
