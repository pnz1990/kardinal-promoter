---
section: Changed
---

- **A Pipeline reads only the Pipelines of its own repository** (#1684) — to check `PathConflict` and the rendered branch conflict, every Pipeline reconcile listed and deep-copied every Pipeline in the cluster, and so did the watches that re-check them on each Pipeline event: in the release-candidate soak (100 Pipelines, 9,000 Bundles over 33 minutes) the reconcile's list alone allocated 69 GB of the controller's 685 GB, and in a 10-minute soak of 3,000 Bundles the reconcile's list and the watch's took 32 GB and 46 GB of 291 GB. The controller now indexes Pipelines by repository and branch, and each check reads only the Pipelines that write the same repository. The conditions are unchanged.
