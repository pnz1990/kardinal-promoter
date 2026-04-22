# Spec: issue-1008 — CHANGELOG.md missing version sections for v0.1.0–v0.8.1

## Design reference
- N/A — documentation maintenance with no user-visible behavior change to the software itself
  (CHANGELOG.md is a user-facing artifact — its absence is a documentation gap)

---

## Zone 1 — Obligations

**O1** — `CHANGELOG.md` has a version section for every git tag from `v0.1.0` through `v0.8.1`, in reverse chronological order (newest first).

**O2** — Each version section follows the format `## [vX.Y.Z] — YYYY-MM-DD` using the tag creation date.

**O3** — Each version section contains a brief summary of the significant features, fixes, or changes in that release (minimum 3 bullets for minor releases, minimum 1 bullet for patch releases).

**O4** — The existing `## [Unreleased]` section is preserved and remains at the top.

**O5** — The CHANGELOG entries are accurate to the actual git history (derived from `git log prevTag..tag --oneline --no-merges`).

---

## Zone 2 — Implementer's judgment

- How detailed to make each entry: aim for the 5–8 most significant user-visible changes per minor release; patch releases can be 1–3 items.
- Whether to include PR numbers: yes, for traceability.
- Whether to include chore/state/agent-loop commits: no — only user-visible changes (feat/fix/docs/refactor in pkg/, cmd/, web/, chart/, docs/).

---

## Zone 3 — Scoped out

- Automated future CHANGELOG maintenance (SM §4a already handles Unreleased)
- Backfilling per-commit PR body descriptions
- Semantic versioning explanation
