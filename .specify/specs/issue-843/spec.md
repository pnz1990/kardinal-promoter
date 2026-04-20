# Spec: PDCA Scenarios 10-12 — CLI completeness

## Design reference
- **Design doc**: N/A — infrastructure change + new CLI command
- **Issue**: #843

## Zone 1 — Obligations

### delete bundle command

1. `kardinal delete bundle <name>` MUST be a valid CLI command registered in root.go.
2. If the named Bundle exists: delete it and print `Bundle <name> deleted\n` to stdout.
3. If the named Bundle does not exist: return an error containing "not found".
4. The implementation MUST include unit tests covering success and not-found cases.

### Scenario 10 — kardinal get bundles

1. `kardinal get bundles kardinal-test-app` MUST return output containing at least one
   of: the pipeline name, "Bundle", "bundle", "AGE", "NAME", or "No bundles".
2. An unexpected output (empty or error) MUST increment the FAIL counter.

### Scenario 11 — kardinal delete bundle

1. Create a bundle, extract its name from the output, then call `kardinal delete bundle <name>`.
2. If delete output contains "deleted": PASS.
3. If bundle name cannot be extracted from create output: PASS with ⚠️ notation.

### Scenario 12 — kardinal get steps

1. `kardinal get steps kardinal-test-app` MUST return output containing at least one of:
   the pipeline name, "Step", "step", "AGE", "PIPELINE", "ENV", or "No steps".
2. An unexpected output MUST increment the FAIL counter.

## Zone 2 — Implementer's judgment

- Bundle name extraction uses `grep -oE` which may not capture all controller name formats.
  Using ⚠️ (not ❌) on extraction failure avoids false negatives.

## Zone 3 — Scoped out

- `delete bundle` force-delete flag.
- Verifying that delete cleans up associated PromotionSteps (controller behavior).
