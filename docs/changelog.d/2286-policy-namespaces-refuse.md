---
section: Changed
---

- **`spec.policyNamespaces` refuses a namespace that has not agreed to share its PolicyGates** (#2286). v0.13 warned with the condition `PolicyNamespacesShared=False` and still read such a namespace's gates; this release doesn't read them.
  - The Pipeline is `Ready=False` with reason `PolicyNamespaceNotShared`, and `status.policyNamespaces.notShared` names the namespace.
  - New versions of the Pipeline wait. They stay `Available` with `Ready=False` and a `PolicyNamespaceNotShared` Warning Event, until the namespace agrees (the label `kardinal.io/policy-shareable=true`, or `controller.policyShareableNamespaces`) or the entry is removed. None fails, and none needs shipping again.
  - A version already promoting keeps the gates it started with. When its Graph is rebuilt, the controller copies that namespace's gates from the Graph rather than reading the namespace, and the Bundle gets a `PolicyNamespaceNotShared` Warning Event saying so. If that Graph is deleted, it isn't rebuilt until the namespace agrees. Until the controller has checked a new generation of the Pipeline, a version in flight isn't rebuilt either (`GraphSynced=False`, reason `PolicyNamespacesUnchecked`).
  - `kardinal policy simulate` reports a new version as `Blocked by: spec.policyNamespaces`. For a version in flight, it uses the gates that version kept.
