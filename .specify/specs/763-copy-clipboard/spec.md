# Spec: Copy-to-clipboard on resource names (#763)

## Zone 1 — Obligations

1. **Pipeline name in sidebar** has a copy button. Clicking copies the pipeline name.
2. **Active bundle name** (in the pipeline detail header) has a copy button.
3. **Commit SHA** in NodeDetail has a copy button.
4. **All copy buttons use the existing `CopyButton` component** — no new copy logic.
5. **No TypeScript errors.**
6. **Apache 2.0 header on changed files.**

## Zone 2 — Implementer's Judgment
- Whether to show copy buttons always or on hover
- Exact placement within each UI element

## Zone 3 — Scoped Out
- Step ID copy in StageDetailPanel (minor benefit)
- Copy on environment names, gate names
