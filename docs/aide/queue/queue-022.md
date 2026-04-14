# Queue 022 — Journey 7: Multi-Tenant Self-Service (ApplicationSet + J7 Test)

> Created: 2026-04-14
> Status: Active
> Purpose: Complete Journey 7 — the final remaining journey for project completion

## Context

6/7 journeys pass. J7 (multi-tenant self-service) requires:
1. examples/multi-tenant/ with working ApplicationSet + Pipeline template
2. docs/advanced-patterns.md documenting the pattern
3. TestJourney7MultiTenant test (fake client, like J1-J6)

The ApplicationSet approach: a root AppSet watches a "teams/" directory in a Git repo.
When a new service folder appears (with pipeline-values.yaml), the AppSet creates:
- A Namespace for the team
- A kardinal Pipeline CRD using the values from pipeline-values.yaml
- Argo CD Applications for each environment

## Items

| Item | Issue | Title | Priority | Size | Depends on |
|---|---|---|---|---|---|
| 900-j7-appset-pipeline | docs/examples | feat(multi-tenant): ApplicationSet + Pipeline template + J7 test | high | l | — |

## Notes

The ApplicationSet template should use the `git` generator to watch a teams/ directory.
The Pipeline CRD template within the ApplicationSet is populated from pipeline-values.yaml.

The J7 test (TestJourney7MultiTenant) verifies:
- Pipeline CRD created in team namespace
- Org PolicyGates from platform-policies namespace are inherited
- `kardinal get pipelines --namespace payment-service` returns the team pipeline
- RBAC: team cannot see pipelines in other namespaces

No new Go code needed — the ApplicationSet is pure YAML, and J7 test uses fake client.
