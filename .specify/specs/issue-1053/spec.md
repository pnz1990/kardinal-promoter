# Spec: issue-1053 — Self-improvement tracking: skills library growth rate

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: "Self-improvement tracking: skills library growth rate" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **O1**: SM batch report `<details>` block must include a line: `- Skills: N (last added: YYYY-MM-DD)` where N is the count of skill files in `~/.otherness/agents/skills/` (excluding PROVENANCE.md and README.md).

2. **O2**: `SKILLS_LAST_ADDED` must be read from PROVENANCE.md — the most recent `## YYYY-MM-DD` header date.

3. **O3**: If `last_added` date is more than 14 days ago: the line must append ` ⚠️ No new skill in Nd — run /otherness.learn`.

4. **O4**: Fail-open: if `~/.otherness/agents/skills/` does not exist or PROVENANCE.md is unreadable, show `skills: ? (last added: unknown)`.

---

## Zone 2 — Implementer's judgment

- Whether to count subdirectories as separate skills
- Exact format of the staleness warning message

---

## Zone 3 — Scoped out

- Writing skills count to metrics.md (separate item)
- Automatic invocation of /otherness.learn (separate Future item)
- Changes to any Go source code
