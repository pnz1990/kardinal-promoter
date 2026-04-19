# Spec: docs — fix stale 06-kardinal-ui.md design doc

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future`
- **Implements**: Move 6 implemented UI items from 🔲 Future to ✅ Present (#802)

## Zone 1 — Obligations

- O1: The 6 implemented components are moved from `## Future` to `## Present` with PR references
- O2: The 3 genuinely unimplemented items remain in `## Future`
- O3: The "Interaction Model" section is updated to reflect that the UI is no longer read-only
  (ActionBar provides pause/resume/rollback actions)
- O4: The 6 items removed from Future are: Fleet-wide health dashboard, Per-pipeline operations
  view, Per-stage detail, In-UI actions, Bundle promotion timeline (rollback/audit), Policy gate detail

## Zone 2 — Implementer's judgment

- PR reference format for each: `(PR #NNN, date)` inline after description
- Do not rewrite the entire doc — make minimal accurate changes

## Zone 3 — Scoped out

- Updating the Package Structure section (still shows old file layout)
- Updating the Views section (still describes the Phase 1 design)
- Responsive layout and `/` keyboard shortcut — tracked in #799 and #800
- Virtualization — still genuinely unimplemented
