# Spec: issue-1134 — QA docs gate

## Design reference
- **Design doc**: `docs/design/41-published-docs-freshness.md`
- **Section**: `§ Future`
- **Implements**: QA docs gate (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **Script exists**: `scripts/qa-docs-gate.sh` exists and is executable.

2. **PR number required**: The script accepts a PR number as `$1` (or `$PR_NUM` env var);
   when neither is provided it prints a skip message and exits 0 (fail-open).

3. **Detect Future-to-Present moves**: The script reads the PR diff (via `gh pr diff`)
   and detects when any `docs/design/*.md` file has a line changed from
   `- 🔲 ` to `- ✅ ` (regex: `^-.*🔲` removed, `^\+.*✅` added).

4. **User-visible feature classification**: For each detected Future-to-Present move,
   the script classifies whether the feature is user-visible by checking the item
   description for keywords: `CLI`, `CRD`, `UI`, `API`, `command`, `flag`, `endpoint`,
   `spec.`, `status.`, `dashboard`, `web`.

5. **Docs update check**: For user-visible items, the script checks whether the PR diff
   also includes changes to any file under `docs/` (excluding `docs/design/`).

6. **Layer 1 auto-documented exemption**: If the design doc item description contains
   `Layer 1` or `auto-documented`, the check passes without requiring docs/ changes.

7. **WRONG output**: When a user-visible feature is detected with no docs/ changes and
   no Layer 1 exemption, the script prints:
   `[QA §3b-docs-gate] WRONG — <item desc>: user-visible feature promoted to ✅ Present
   but no docs/ file was updated. Add or update a docs/ page for this feature.`
   and exits **1**.

8. **PASS output**: When all detected moves pass the check, the script prints:
   `[QA §3b-docs-gate] PASS — <N> Present items checked, <M> docs/ files verified.`
   and exits **0**.

9. **Fail-open**: Any error (no `gh` available, PR not found, network failure) causes
   the script to print a skip message and exit **0**. It never blocks on its own failure.

10. **Copyright header**: File starts with Apache 2.0 copyright header.

---

## Zone 2 — Implementer's judgment

- Exact regex pattern for detecting 🔲/✅ transitions is left to implementer.
- "Layer 1 auto-documented" keyword list can be extended.
- Whether to run `gh pr diff --name-only` first (fast path) or full diff is left to implementer.

---

## Zone 3 — Scoped out

- This script does NOT auto-fix anything — it is read-only.
- It does NOT check whether the docs content is accurate, only whether a docs file changed.
- It does NOT run in CI automatically (that is a future item).
- It does NOT post GitHub comments — it only writes to stdout.
