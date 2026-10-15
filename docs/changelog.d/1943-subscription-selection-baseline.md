---
section: Fixed
---

- **Editing a Subscription's source or tag selection no longer promotes what the new selection picks** (#1943, #1999, [Subscription](subscription.md#deduplication)). These edits used to create a version on the next poll, which could be an older build or commit (for example `Lexical` instead of `NewestBuild`, a lagging mirror, or another branch) and would roll the environments back:
  - for an image Subscription: `registry`, `strategy`, `tagFilter`, `excludeTagFilter`, `allowTags`, `ignoreTags`, `semverConstraint` or `discoveryLimit`;
  - for a Helm Subscription: `repoURL`, `chart` or its filters;
  - for a Git Subscription: `repoURL` or `branch`.

  The edit now records a new baseline (`status.observedSelection`) and creates no version, as a `pathGlob` edit does.
