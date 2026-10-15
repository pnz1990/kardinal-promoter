---
section: Fixed
---

- **Editing an image Subscription's tag selection no longer promotes what the new selection picks** (#1943, [Subscription](subscription.md#deduplication)). Changing `strategy`, `tagFilter`, `excludeTagFilter`, `allowTags`, `ignoreTags`, `semverConstraint` or `discoveryLimit`, or a Helm Subscription's filters, used to create a version on the next poll. That version could be an older build, for example `Lexical` instead of `NewestBuild`, and would roll the environments back. The edit now records a new baseline (`status.observedSelection`) and creates no version, as a `pathGlob` edit does.
