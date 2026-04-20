# Spec 881 — Add data-testid="node-detail" to NodeDetail and un-skip E2E tests

## Design reference
- **Design doc**: `docs/design/14-v060-roadmap.md`
- **Section**: `§ Future`
- **Implements**: 14.6 — PDCA: fix broken Playwright scenarios S25-S27 (rollback button) 🔲 → ✅

---

## Zone 1 — Obligations

**O1** — `NodeDetail` root container element has `data-testid="node-detail"`.
Violation: `page.locator('[data-testid="node-detail"]')` returns 0 elements.

**O2** — The two `test.skip` calls in `web/test/e2e/journeys/011-rollback-button.spec.ts`
are replaced with active `test` calls. The TODO comments are removed.
Violation: file still contains `test.skip` for Step 2 or Step 3.

**O3** — The existing `NodeDetail.test.tsx` unit tests all still pass.
Violation: `npm test` fails for NodeDetail.test.tsx.

**O4** — The build (`go build ./...`) and lint (`go vet ./...`) pass without changes
to Go source files (this is a frontend-only change).
Violation: Go build fails after this PR.

---

## Zone 2 — Implementer's judgment

- Where exactly to put `data-testid`: on the outermost `<div>` returned by NodeDetail.
- Whether to also add `aria-label="Node detail panel"` for screen reader accessibility.
  (Recommended but not obligated.)

---

## Zone 3 — Scoped out

- Fixing any other skipped Playwright tests beyond 011-rollback-button.spec.ts.
- Changing the visual appearance or layout of NodeDetail.
- Adding additional data-testid attributes beyond the root element.
