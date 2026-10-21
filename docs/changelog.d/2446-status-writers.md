---
section: Security
---

- **Only the controller writes kardinal status** (#2446) — a new `<fullname>-status-writers` admission policy refuses every status update of Bundles, ChangeRequests, MetricChecks, NotificationHooks, Pipelines, PolicyGates, RollbackPolicies, ScheduleClocks, ScmProviders and Subscriptions by anyone but the controller, cluster-admins included. Before, a namespace admin could, for example, set a gate instance's `status.ready` and start a promotion past an org gate with no override record: RBAC's `resources: ["*"]` covers the status subresource. `<fullname>-admin` now names its kinds and grants no status. Cluster-scoped kinds' status is trusted to holders of cluster-wide RBAC ([Status is the controller's](guides/security.md#status-is-the-controllers)).
