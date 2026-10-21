---
section: Added
---

- **Batched writes for many environments on one branch** (#2366). With `spec.git.writes: batched`, the `approval: auto` environments of a Bundle that are ready together are written in one round of the Bundle's new `BranchWrite`: one clone, one commit per environment (each keeps its own commit, receipt and revert) and one push, instead of one clone and one push per environment in turn. Each batched step runs `await-write, health-check`. A pause, a freeze or an environment hold stops a batched write as it stops a step, checked when the round starts and again right before its push; a round interrupted after its push intent is resumed from the commit SHAs it recorded in `BranchWrite` status. Batching applies to directory-layout environments of image Bundles updated by kustomize, helm or yaml, without a fleet, and uses the compact Graph shape. See [Batched writes](pipeline-reference.md#batched-writes-specgitwrites-batched).
