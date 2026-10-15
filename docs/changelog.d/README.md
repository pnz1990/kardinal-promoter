# Unreleased changelog entries

Each change that users see adds one file here, so two pull requests never
edit the same lines of `docs/changelog.md`. Name the file
`<issue or PR number>-<slug>.md`, for example `1835-fleet-pause.md`:

```markdown
---
section: Added
---

- **Fleet operations** (#1835) — what changed, for a user, with links to the
  docs pages relative to docs/ (`pause.md#pause-one-fleet`).
```

`section` is one of Upgrade (shown as "Before you upgrade"), Added, Changed,
Deprecated, Removed, Fixed, Security, Documentation or Development. A file may
hold several entries of one section. The docs site lists these files under
[Unreleased] (`hack/docs-changelog/changelog_fragments.py`).

`go run ./hack/changelog unreleased` prints the section as it will be
released. `make changelog-release VERSION=vX.Y.Z` writes the entries into
`docs/changelog.md` under that version and deletes these files, before the
tag. CI's `changelog` check fails a pull request that changes the product, the
chart, the CRDs, the UI or the user docs without a file here, unless the
pull request is labelled `no-changelog`.
