# Spec: ADOPTERS.md

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future` — Lens 5: Adoption
- **Implements**: No ADOPTERS.md or case studies (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

### O1 — ADOPTERS.md exists at repository root
`ADOPTERS.md` MUST exist at the root of the repository after this PR merges.
- Violation: file absent from repository root.

### O2 — First entry is the PDCA self-use case
The file MUST contain at least one entry describing kardinal-promoter's own PDCA validation loop.
- The entry MUST mention `kardinal-test-app` and `kardinal-demo`.
- Violation: file exists but contains no PDCA self-use entry.

### O3 — Format is a Markdown table
The ADOPTERS.md MUST use a Markdown table with at minimum columns: Organization, Use Case, Added.
- Violation: file exists but uses a non-table format that is not scannable.

---

## Zone 2 — Implementer's judgment

- Exact wording of the self-use entry.
- Whether to include an "EKS" environment row or a single combined row.
- Whether to add a "Contributing" section explaining how to add an entry.

---

## Zone 3 — Scoped out

- Creating GitHub Discussions — separate issue #979.
- Soliciting external adopters — that is a community outreach task, not an engineering task.
