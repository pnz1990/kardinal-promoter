# Spec: issue-1188

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Present`
- **Implements**: Fix state clobber in zero-pr-detect.sh (🔲 → ✅, bug fix)

---

## Zone 1 — Obligations (falsifiable)

**O1**: In both Python state-write blocks in `scripts/zero-pr-detect.sh`, the `checkout` command includes BOTH `.otherness/dry-run-state.json` AND `.otherness/state.json`.
- Verification: `grep -c "state.json" scripts/zero-pr-detect.sh` returns ≥4 (at least 2 checkout lines, 2 add lines — both in reset and increment blocks).

**O2**: The git `add` in both blocks adds only `dry-run-state.json` (not `state.json`), so only the dry-run file is committed while `state.json` is preserved in the tree without being re-committed as changed.
- Verification: `grep "git.*add.*state.json" scripts/zero-pr-detect.sh` returns 0 matches in the add lines; the checkout is for read-only preservation.

**O3**: The fix does not change any other behavior of the script — only the checkout step is extended.
- Verification: diff shows only `checkout` lines changed in the two Python heredoc blocks.

## Zone 2 — Implementer's judgment

The correct fix: change both `git -C state_wt checkout _state -- .otherness/dry-run-state.json` calls to also include `state.json`. This ensures the worktree's tree has both files, so the new commit preserves `state.json` at its previous value while updating `dry-run-state.json`.

Do NOT add `state.json` to the `git add` call — only `dry-run-state.json` should be staged. The checkout of `state.json` is purely to populate the tree so it appears in the commit.

Wait — this is wrong. `git -C wt add target` adds only the `dry-run-state.json` file. The worktree tree initially has no files (--no-checkout). After `checkout _state -- dry-run-state.json`, the tree has only that one file. When we `commit`, the tree snapshot only has `dry-run-state.json`. To fix: we need to add `state.json` to the git index of the worktree too. So we checkout both files AND add both to the index before committing, but only write new content to `dry-run-state.json`.

Correct approach:
1. `git checkout _state -- .otherness/dry-run-state.json .otherness/state.json`
2. Write new content only to `dry-run-state.json`
3. `git add .otherness/dry-run-state.json .otherness/state.json` (add both to preserve tree)
4. Commit

OR simpler: since `state.json` wasn't modified, just add it explicitly after checkout.

## Zone 3 — Scoped out

- Refactoring the overall state management pattern
- Adding retry logic to the push
- Fixing other scripts that may have similar patterns
