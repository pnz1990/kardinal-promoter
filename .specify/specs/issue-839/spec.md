# Spec: PDCA anchor score comment

## Design reference
- **Design doc**: N/A — infrastructure change with no user-visible behavior
- **Issue**: #839

## Zone 1 — Obligations

1. After each PDCA run, the workflow MUST post a structured comment to Issue #1 in the format:
   `[ANCHOR | kardinal-promoter | DATE] coverage: N/M (X%) | PASS=A FAIL=B`
   where N = scenarios passed, M = total scenarios run, X = percentage, A = pass count, B = fail count.
2. The anchor comment MUST be a separate `gh issue comment` call after the main PDCA evidence comment.
3. DATE MUST be UTC format `YYYY-MM-DD`.
4. The anchor comment MUST be posted even if some scenarios fail (step runs with `if: always()`).

## Zone 2 — Implementer's judgment

- Percentage is integer (floor division).
- The main PDCA evidence comment format is unchanged.

## Zone 3 — Scoped out

- Changing the set of scenarios (tracked in #841–843).
- Parsing historical anchor comments.
