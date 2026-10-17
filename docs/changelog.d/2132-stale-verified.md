---
section: Fixed
---

- **A version is no longer recorded Verified in an environment a newer version has already replaced** (#2132). Seen in the scale suite under API-server throttling, and present in v0.12.0.
  - **What went wrong:** an older version's step still health-checking an environment could pass after a newer version pushed there and was Verified. It was then recorded Verified with a `PromotionSucceeded` AuditEvent, for a version the environment no longer ran. This happened when health was checked on the Deployment (`resource`) and its images could not name the version (kustomize `newName`, or a config version).
  - **Superseded, not Verified:** a step still health-checking when a newer version's step has pushed to the same environment ends `Superseded`, with a `PromotionSuperseded` record. It is decided from the newer step's recorded push, even when the Bundle's supersession is written late.
  - **The branch must still hold the commit:** when the images cannot name the version, the `resource` check requires that the environment's branch still holds the promotion's commit with the files it wrote, as `argocd` and `flux` already do (#1575).
  - See [Health adapters](health-adapters.md).
