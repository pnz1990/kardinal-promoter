---
section: Fixed
---

- **An agent scoped nowhere is refused with its scope** (#1867) — the `<release>-actor` admission policy refused an AgentIdentity with an empty `scope.namespaces` ("nowhere") with the expression error `no such key: namespaces`. It now refuses it with "agent … may not act here".
