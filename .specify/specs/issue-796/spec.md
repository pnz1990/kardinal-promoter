# Spec: fix(ci) demo-validate workflow — issues:write permission

## Design reference
- N/A — infrastructure change with no user-visible behavior

## Zone 1 — Obligations

- O1: `.github/workflows/demo-validate.yml` `permissions` block includes `issues: write`
- O2: No other permissions are added beyond those already present (`contents: read`, `packages: read`)
- O3: The "Post result to issue" step succeeds when `github.event_name == 'schedule'`

## Zone 2 — Implementer's judgment

- Where to place `issues: write`: at the top-level `permissions` block (affects all jobs)
  vs job-level `permissions` block (affects only `demo-validate` job). Job-level is more
  principled but the workflow has only one job, so top-level is acceptable.

## Zone 3 — Scoped out

- Changing the notification message format
- Changing when the step runs (already guarded by `schedule` event)
- Fixing Node.js 20 deprecation warnings (separate concern)
