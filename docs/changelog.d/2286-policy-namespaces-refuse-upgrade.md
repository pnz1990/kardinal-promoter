---
section: Upgrade
---

- **`spec.policyNamespaces` now refuses a namespace that has not agreed to share its PolicyGates** (#2286). v0.13 warned with the condition `PolicyNamespacesShared=False`.
  - **Before upgrading,** run `kardinal doctor`, or the `kubectl get pipelines -A …` command in [Sharing gates with other namespaces](policy-gates.md#sharing-gates-with-other-namespaces). For each namespace it names, label it `kardinal.io/policy-shareable=true`, list it in `controller.policyShareableNamespaces`, or remove it from the Pipeline.
  - **After upgrading,** a Pipeline that still names one is `Ready=False` (reason `PolicyNamespaceNotShared`). Its new versions wait, naming the namespace, until it agrees or the entry is removed. Versions already promoting finish with the gates they started with. Nothing they had is dropped, so no gate is skipped.
  - **Upgrading from v0.12 directly:** v0.12 gave no warning, so run the same check before upgrading.
