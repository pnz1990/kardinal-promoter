---
section: Upgrade
---

- **`receipts.sinks.oci.perNamespace` defaults to `off`** (#2285). That keeps v0.13's controller-wide `allowedRepositories` alone, so an upgrade changes nothing about where receipt referrers are pushed until an operator sets `narrow` or `required` ([Per namespace](receipts.md#per-namespace))
