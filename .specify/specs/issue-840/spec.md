# Spec: PDCA daily cadence

## Design reference
- **Design doc**: N/A — infrastructure change with no user-visible behavior
- **Issue**: #840

## Zone 1 — Obligations

1. The PDCA workflow `pdca.yml` schedule cron expression MUST be `0 2 * * *` (daily at 02:00 UTC).
2. The prior cron expression `0 4 * * 0` (weekly Sunday only) MUST be removed.
3. All other workflow behavior MUST remain unchanged.

## Zone 2 — Implementer's judgment

- Comment text updated to explain daily cadence rationale.

## Zone 3 — Scoped out

- Changing the workflow trigger inputs or scenario list.
- Adding new scenarios (tracked in #841–843).
