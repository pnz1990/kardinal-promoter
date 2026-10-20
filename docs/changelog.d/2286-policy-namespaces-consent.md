---
section: Security
---

- **`spec.policyNamespaces` asks the named namespace's consent** (#2286). A Pipeline could read any namespace's PolicyGates through `spec.policyNamespaces`, another team's included: the controller copied them into the Pipeline's namespace with its own access. A namespace other than the org policy namespaces now agrees with the label `kardinal.io/policy-shareable: "true"` or by being listed in `controller.policyShareableNamespaces` (`--policy-shareable-namespaces`). A Pipeline naming one that has not gets `PolicyNamespacesShared=False` and a `PolicyNamespaceNotShared` Warning Event; in this release its gates are still read ([Sharing gates with other namespaces](policy-gates.md#sharing-gates-with-other-namespaces)).
