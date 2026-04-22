# Spec: issue-1046 — Single-page health dashboard at REPORT_ISSUE

## Design reference
- **Design doc**: `docs/design/13-scheduled-execution.md`
- **Section**: `§ Future`
- **Implements**: Single-page health dashboard at Issue #1 (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — The SM §4f must maintain a single comment on `REPORT_ISSUE` with a `[HEALTH SNAPSHOT]`
sentinel. If the comment does not exist, it is created. If it exists, it is edited in-place
using `gh api --method PATCH repos/$REPO/issues/comments/$COMMENT_ID`.

**O2** — The health snapshot comment MUST contain all of:
- `loop=GREEN|RED|STALL` (current HEALTH value)
- `pdca=<status>` (from PDCA_STATUS)
- `last_pr=#NNN "<title>"` (most recent feat/fix/refactor PR)
- `queue=N` (TODO count)
- `date=<YYYY-MM-DD>` (UTC date of this update)

**O3** — The health snapshot comment is updated even when HEALTH=RED, so a human can see
the degraded state without reading all previous batch comments.

**O4** — The mechanism is fail-open: if the API call to find/create/edit the comment fails,
the SM logs a non-fatal warning and continues. The health snapshot is a convenience feature;
it must never block the batch report.

**O5** — The `[HEALTH SNAPSHOT]` sentinel must be unique enough to avoid matching other
comments. Use the exact string `<!-- otherness-health-snapshot -->` as an HTML comment
marker embedded in the comment body for machine-findability.

---

## Zone 2 — Implementer's judgment

- Where to insert in SM: after §4f health computation, before the batch report post.
- Comment search: use `gh api repos/$REPO/issues/$REPORT_ISSUE/comments --paginate --jq`
  to find the most recent comment containing the sentinel. Only need to check last 100.
- Edit vs create: if found, PATCH; if not found, create a new comment.
- Format: single-line machine-readable header + optional human-readable table.

---

## Zone 3 — Scoped out

- Machine-readable structured JSON format (separate Future item in doc 13)
- PDCA journey count (X/Y format) — requires parsing pdca.yml outputs
- Archiving old comments (separate Future item)
