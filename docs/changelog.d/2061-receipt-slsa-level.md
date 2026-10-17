---
section: Added
---

- **A promotion receipt claims the version's SLSA build level** (#2061): `kardinalPromotion.checks.slsa` (the level, its source, the images folded) from the newest Verified ImageVerification, the lowest of its images' as gates fold `attestations.<name>.slsa.buildLevel`, and `SLSA_BUILD_LEVEL_<n>` in `verifiedLevels`, so `kardinal verify-receipt --require SLSA_BUILD_LEVEL_3` admits only a version built at level 3 ([Receipts](receipts.md))
