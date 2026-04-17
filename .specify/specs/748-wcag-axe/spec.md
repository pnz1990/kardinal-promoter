# Spec: WCAG 2.1 AA Automated Check — axe-core in CI (#748)

## Zone 1 — Obligations (falsifiable)

1. **`@axe-core/playwright` is installed** as a dev dependency.
   _Violation_: Package missing from `package.json`.

2. **A Playwright accessibility spec exists** at
   `web/test/e2e/journeys/009-accessibility.spec.ts` that:
   - Navigates to the dashboard root
   - Calls `checkA11y` from `@axe-core/playwright`
   - Uses `wcag2aa` tag to scope to WCAG 2.1 AA rules
   - Passes (zero violations) on the mock API server
   _Violation_: File does not exist, or test is skipped/commented out.

3. **The test is registered in the CI `frontend build` job.**
   The existing `test:e2e` npm script already runs all Playwright specs.
   No CI file changes are required if the spec follows the existing file pattern.
   _Violation_: Test is unreachable from `bun run test:e2e`.

4. **The test runs against the existing mock API server** (no real cluster required).
   _Violation_: Test requires a running controller or Kubernetes cluster.

5. **Copyright header present** in the new spec file.
   _Violation_: Header missing.

## Zone 2 — Implementer's Judgment

- Which axe impact levels to fail on: `critical` and `serious` violations block
  the test; `moderate` and `minor` are logged but do not fail.
- Whether to test multiple states (pipeline selected, node detail open): single
  default state is sufficient for this item.
- Test timeout: use the default Playwright timeout (30s).

## Zone 3 — Scoped Out

- Manual a11y audit beyond axe-core automation.
- Screen reader testing (requires a browser extension, not automatable in CI).
- Color contrast audit beyond what axe reports (visual-only check).
- WCAG 2.2 rules (axe defaults to 2.1 AA for now).
- Focus trap testing for the KeyboardShortcutsPanel modal.
