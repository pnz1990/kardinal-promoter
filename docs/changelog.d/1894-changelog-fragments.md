---
section: Development
---

- **No more merge conflicts in the changelog and coverage.tsv** (#1894) — a change's changelog entry is a file of its own, `docs/changelog.d/<issue>-<slug>.md`. The docs site lists those files under [Unreleased], `make changelog-release VERSION=vX.Y.Z` writes them into `docs/changelog.md` before a tag, and CI's `changelog` check fails a user-facing pull request without one, unless it is labelled `no-changelog`. `test/e2e/coverage.tsv` cites sources by Go declaration, markdown heading or text on one line instead of line numbers, which every edit above them moved, and lists its rows by id: two unrelated pull requests no longer conflict on it.
