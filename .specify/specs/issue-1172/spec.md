# Spec: issue-1172

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: Pressure context is written by the same agents who evaluate it — conflict of interest (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `scripts/check-pressure-context-drift.sh` exists, is executable, exits non-zero when `otherness-scheduled.yml` has a change to the "Context for this vision scan:" block relative to HEAD~1.
Verify: `test -x scripts/check-pressure-context-drift.sh && bash -n scripts/check-pressure-context-drift.sh`

**O2**: Script exits 0 (no-op) when the "Context for this vision scan:" block has NOT changed.
Verify: run the script on an unchanged workflow file → exits 0.

**O3**: Script prints `[HUMAN REVIEW REQUIRED: pressure context changed]` when the block has changed.
Verify: in a repo where the block was modified in the latest commit, run the script → output contains `HUMAN REVIEW REQUIRED`.

**O4**: Script is fail-safe: exits 0 when the workflow file does not exist.
Verify: `WORKFLOW_FILE=/nonexistent.yml bash scripts/check-pressure-context-drift.sh` → exits 0 with `not found`.

**O5**: Script has Apache 2.0 license header.
Verify: `head -3 scripts/check-pressure-context-drift.sh | grep -q 'Apache License'`

## Zone 2 — Implementer's judgment

- Use `git diff HEAD~ -- <workflow>` to detect changes between last commit and current
- The "Context for this vision scan:" block is the specific marker to watch
- On first commit (no HEAD~), exit 0 safely
- ci.yml integration is deferred (requires `workflows` permission which the GitHub App lacks per issue #1177)

## Zone 3 — Scoped out

- ci.yml integration (requires workflows permission — tracked in issue #1177)
- Blocking PR merge (alarm, not hard gate)
- Detecting changes in files other than otherness-scheduled.yml
- Detecting agent-authored vs human-authored changes
