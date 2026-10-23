---
section: Security
---

- **Receipt referrers can be held to their namespace's repositories** (#2285). `receipts.sinks.oci.perNamespace` (`--receipt-oci-per-namespace`) is `off`, `narrow` or `required`. A namespace's repositories come from `receipts.sinks.oci.namespaceRepositories` (a bad entry stops the controller at start) and, unless `namespaceAnnotation: false`, from its Namespace's `kardinal.io/receipt-oci-repositories` annotation. `allowedRepositories` stays the outer bound. An image the namespace may not push gets no referrer (`Stored` `False`, `RepositoryNotAllowed`, naming the namespace), and an unreadable Namespace pushes nothing and is retried. **Upgrade:** the default is `off`, which keeps v0.13's controller-wide list alone, so nothing changes until an operator sets it. A multi-tenant install sets `required`, lists every team and sets `namespaceAnnotation: false` ([Per namespace](receipts.md#per-namespace))
