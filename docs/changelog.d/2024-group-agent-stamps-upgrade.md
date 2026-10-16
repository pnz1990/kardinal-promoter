---
section: Upgrade
---

- **Group-registered agents' holds, pause/resume and rejections are recorded without an actor stamp** (#2024) — the AuditEvent of a hold, a pause or resume, or a rejection by an agent registered through a group names the user but carries no `spec.actor`, and its message says why: the controller cannot see the writer's groups to confirm the agent. Register the agent's ServiceAccount or user directly if those records must name it ([Audit logging](guides/security.md#audit-logging)).
