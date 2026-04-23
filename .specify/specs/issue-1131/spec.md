# Spec: issue-1131 — Automated Kargo community issue monitoring

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 7`
- **Implements**: **Kargo community issue monitoring is not automated** (🔲 → ✅)

## Zone 1 — Obligations

1. `scripts/kargo-gap-check.sh` exists and is executable.
2. The script: (a) fetches the 20 newest open issues from `akuity/kargo` with labels `kind/feature` or `enhancement`; (b) cross-references against existing `🔲` items in `docs/design/15-production-readiness.md`; (c) outputs any Kargo feature request with >5 thumbsup reactions that has no matching keyword in the doc-15 content.
3. The script exits 0 on success (even with no new gaps found) and non-zero only on API/IO errors.
4. `docs/design/15-production-readiness.md` moves the item from `🔲` to `✅` with a PR reference.

## Zone 2 — Implementer's judgment

- Implementation: bash script using `gh api` for Kargo issues
- Cross-reference: keyword matching against doc-15 content (not exact match)
- Output format: plaintext summary, suitable for PM batch report inclusion
- Thumbsup reactions: use `gh api` to read reaction counts

## Zone 3 — Scoped out

- Not automatically creating issues from gaps (PM reviews output and decides)
- Not integrating into the otherness pm.md phase file (that's ~/.otherness, out of scope)
- Not real-time monitoring (run on demand or from scheduled PM workflow)
