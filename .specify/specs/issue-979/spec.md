# Spec: issue-979 — Community presence

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 5 — Adoption`
- **Implements**: No community presence (🔲 → ✅)

---

## Zone 1 — Obligations

### O1 — CONTRIBUTING.md
A `CONTRIBUTING.md` file MUST exist at the repository root covering:
- How to build and test locally
- How to submit bug reports and feature requests
- Community channels (link placeholder for GitHub Discussions pending admin enablement)

### O2 — GitHub Discussions note in docs/index.md
`docs/index.md` MUST include a "Community" section with links to:
- GitHub Issues (for bugs/features)
- GitHub Discussions (placeholder note: "coming soon — requires repo admin action")

### O3 — Design doc updated
`docs/design/15-production-readiness.md` must move this item from 🔲 to ✅ with a note
that GitHub Discussions enablement requires repo admin (API cannot enable it from this token).

### O4 — tasks.md created
`tasks.md` created before code.

### O5 — build, tests, lint pass
No Go code changes in this PR; existing tests must still pass.

---

## Zone 2 — Implementer's judgment

- CONTRIBUTING.md can use standard OSS template adapted for this project.
- Exact formatting of community section in docs/index.md is flexible.

---

## Zone 3 — Scoped out

- Enabling GitHub Discussions on the repo (requires admin API access — document as [NEEDS HUMAN]).
- Creating Discord/Slack server.
- Stack Overflow tag.
