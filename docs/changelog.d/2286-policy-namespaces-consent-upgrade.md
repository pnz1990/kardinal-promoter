---
section: Upgrade
---

- **Label the namespaces your Pipelines name in `spec.policyNamespaces`** (#2286). The next release refuses a `spec.policyNamespaces` entry whose namespace has not agreed to share its PolicyGates. Label each such namespace `kardinal.io/policy-shareable=true`, or list it in `controller.policyShareableNamespaces`. The org policy namespaces and a Pipeline's own namespace need nothing. To find the Pipelines that would be refused: `kubectl get pipelines -A -o jsonpath='{range .items[?(@.status.conditions[*].type=="PolicyNamespacesShared")]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'`.
