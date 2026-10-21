---
section: Development
---

- **A removed CRD field fails CI unless the last release deprecated it** (#1996) — `TestReleasedCRDFieldsKept` (`test/hack/api_fields_test.go`) enforces the [deprecation policy](api-stability.md#deprecation-policy).
  - **What it compares.** It reads every field of the newest release's CRDs (`config/crd/bases` at the newest `vX.Y.Z` tag) and checks it against the tree's.
  - **On main.** It fails on a field that is gone, unless that release marked it deprecated: a `Deprecated:` paragraph in its description, or in a field around it, or a CRD version marked `deprecated: true`.
  - **On a `release/vX.Y` branch whose minor is released.** It fails on any field that is gone.
  - **Checked against history.** `TestRemovedFieldsRuleOnHistory` runs the rule from v0.10.0 to v0.11.0, the one minor that removed deprecated fields. It passes, and it reports all 34 removed fields once the marks are cleared.
  - **CI.** The test shards and the docs-tests job fetch the release tags for the guard (`ci.yml`), and in CI it fails without them.
