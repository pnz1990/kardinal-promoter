---
section: Security
---

- **An agent stamp is recorded only when the AgentIndex vouches for it** (#2024) — while the AgentIndex is missing (a fresh install, before the controller builds it), admission cannot check the agent and sponsor of a hold's, a `spec.lastChange`'s or a rejection's stamp, so a person could stamp one as any agent. The controller now checks each stamp against the index before an AuditEvent records it: an agent stamp the index does not confirm, or any agent stamp while it is missing, is not recorded, and the record's message says why; people's and bots' stamps are recorded as before ([Audit logging](guides/security.md#audit-logging)).
