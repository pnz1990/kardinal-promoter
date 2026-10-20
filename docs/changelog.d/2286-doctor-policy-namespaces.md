---
section: Added
---

- **`kardinal doctor` lists the Pipelines a later release refuses for `spec.policyNamespaces`** (#2286). A new check, "Policy namespaces shared", names every Pipeline whose `spec.policyNamespaces` includes a namespace that hasn't agreed to share its PolicyGates (`PolicyNamespacesShared=False`), and the namespaces themselves. It's a WARN against this release, which still reads their gates, and a FAIL against v0.14, which refuses them. Against a controller older than v0.13, which doesn't check, it warns that a clean result proves nothing. Run it before upgrading to v0.14 ([Sharing gates with other namespaces](policy-gates.md#sharing-gates-with-other-namespaces)).
