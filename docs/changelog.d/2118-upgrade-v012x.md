---
section: Documentation
---

- **Upgrading from v0.12.0 and v0.12.1** ([guide](upgrade.md#upgrading-from-v0120), #2118) — the guide's steps to this release are run as written by two new live suites, `upgrade-v0120` and `upgrade-v0121`, on Kubernetes 1.35 and 1.37, while versions are in flight. Versions keep their Graphs, steps and PRs and finish; gates that hold a version keep holding it, and an edited gate reaches it; approvals and Verdicts recorded before the upgrade count, and an AgentRun running at the upgrade finishes and counts; an automatic rollback v0.12.1 started is the same rollback after the upgrade; the PromotionReceipt and ChangeRequest kinds are installed and stay inert until you use them; the reader RoleBindings of v0.12 are removed.
