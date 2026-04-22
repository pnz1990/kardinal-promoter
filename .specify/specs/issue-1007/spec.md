# Spec: issue-1007 — feat(qa): §3b docs gate for user-visible features

## Design reference
- **Design doc**: `docs/design/41-published-docs-freshness.md`
- **Section**: `§41.5 QA docs gate`
- **Implements**: 🔲 QA §3b docs gate: verify user-visible features have docs updates (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. `docs/design/41-published-docs-freshness.md` contains a new §41.5 entry in the Future section describing the QA docs gate.

2. `qa.md` §3b (spec conformance check) contains a new docs-gate step that:
   a. Reads the PR diff to find lines moving items from 🔲 Future to ✅ Present.
   b. For each moved item: classifies whether the feature is user-visible (CLI, CRD, UI) using keyword matching.
   c. For user-visible features: checks if the PR diff includes changes to `docs/` files.
   d. If no `docs/` changes: posts a `WRONG` finding: "User-visible feature shipped with no docs update."
   e. Exception: if the feature is Layer 1 auto-documented (CRD fields auto-generated, CLI auto-completion), the gate passes without docs changes.

3. The check is implemented as a Python block in `qa.md §3b`, wrapped in fail-open try/except.

4. The check runs after the existing spec conformance check (not as a replacement).

5. The implementation includes a test script: `.specify/specs/issue-1007/test_docs_gate.py` with ≥6 tests covering:
   - Detecting user-visible feature items (CLI, CRD, UI keywords)
   - Detecting non-visible features (skipped)
   - Detecting docs/ changes in diff
   - Missing docs/ changes triggers WRONG finding

## Zone 2 — Implementer's judgment

- Keyword list for user-visible classification (CLI, CRD, UI are baseline; can add others).
- Layer 1 auto-documented keywords (e.g. "CRD field auto-generated", "metrics endpoint").
- Whether WRONG finding blocks merge or is advisory (advisory — QA can still approve with justification).
- Section number in qa.md (append after existing §3b content).

## Zone 3 — Scoped out

- Automatically updating docs files.
- Semantic understanding of what "user-visible" means beyond keyword matching.
- Checking docs/aide/ or docs/design/ (only `docs/*.md` customer-facing docs).
