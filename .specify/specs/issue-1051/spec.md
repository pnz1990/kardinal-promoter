# Spec: issue-1051 — Skills Inventory Proactive Discovery at Session Start

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: "Skills inventory is never consulted before starting work — proactive skills discovery is absent" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — `docs/aide/skills-inventory.md` exists and lists at least the skills currently in `~/.otherness/agents/skills/` (excluding PROVENANCE.md and README.md). Each row has: skill name, file path, keyword(s) for matching.
- Violation: the file does not exist or has no rows after this PR merges.

**O2** — COORD reads `docs/aide/skills-inventory.md` at session start (before queue generation), matches skill keywords against the titles of the top 5 queue items, and emits `[SKILL SUGGESTED: <name> — relevant to <area>]` for each matching skill.
- Violation: COORD never emits `[SKILL SUGGESTED:]` even when skills-inventory.md contains matching keywords.

**O3** — The match is keyword-based substring matching (case-insensitive). A skill matches if any of its listed keywords appears in any queue item title.
- Violation: a skill with keyword "reconciler" does not match an item with "reconciler" in the title.

**O4** — Fail-open: if `docs/aide/skills-inventory.md` does not exist, the COORD step skips silently without error.
- Violation: COORD logs an error or crashes when the file is absent.

**O5** — The suggestion is emitted as a log line only. It does not block queue generation or item claiming.
- Violation: COORD does not claim any item because a skill suggestion is pending.

---

## Zone 2 — Implementer's judgment

- Format of the skills-inventory.md: markdown table with columns: Name | File | Keywords | Description.
- The COORD step is a new §1b-skills section, placed after §1b-delta.
- Matching is substring, not full-word; "scm" matches "scm-provider-pattern".
- Maximum 5 suggestions per session to avoid noise.
- The inventory is manually maintained (no auto-update in this PR). 
  The SM §4f-skills-tracking already tracks `SKILLS_COUNT` and `SKILLS_LAST_ADDED` — that's a separate read path.

---

## Zone 3 — Scoped out

- Auto-generating the inventory from `~/.otherness/agents/skills/` on every session (would require shell state update in otherness infra — out of scope for this PR).
- Loading the suggested skill file automatically (this is the human's job — we just suggest).
- Skills from external repos or non-otherness paths.
