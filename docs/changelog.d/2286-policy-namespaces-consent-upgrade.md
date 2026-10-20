---
section: Upgrade
---

- **Label the namespaces your Pipelines name in `spec.policyNamespaces`** (#2286). The next release refuses a `spec.policyNamespaces` entry whose namespace has not agreed to share its PolicyGates. Label each such namespace `kardinal.io/policy-shareable=true`, or list it in `controller.policyShareableNamespaces`. The org policy namespaces and a Pipeline's own namespace need nothing. To list the Pipelines that would be refused, run the `kubectl get pipelines -A ...` command in [Sharing gates with other namespaces](policy-gates.md#sharing-gates-with-other-namespaces); on a cluster where tenants may edit their Namespace's labels, read the same section on who may set the label.
