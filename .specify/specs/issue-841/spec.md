# Spec: PDCA Scenario 7 — config-only promotion

## Design reference
- **Design doc**: N/A — infrastructure change with no user-visible behavior
- **Issue**: #841

## Zone 1 — Obligations

1. The PDCA workflow MUST include a Scenario 7 step after Scenario 6.
2. Scenario 7 MUST test `kardinal create bundle <pipeline> --type config` (no `--image` flag).
3. Scenario 7 MUST PASS when the command output contains "Bundle" or "bundle" or "created".
4. Scenario 7 MUST FAIL (increment FAIL counter) when the command returns an error without creating a bundle.

## Zone 2 — Implementer's judgment

- Config bundle creation with no gitCommit field is a valid no-artifact config promotion.
- The test does not wait for pipeline progression (config-merge step may not have git state to act on).

## Zone 3 — Scoped out

- Testing the full config-merge step execution (requires a configured config source).
- Testing `--git-commit` flag (not yet implemented in CLI).
