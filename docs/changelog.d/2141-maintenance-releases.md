---
section: Development
---

- **Patch releases from a maintenance branch.** `release.yml` accepts a patch tag on `release/vX.Y` whose commit descends from `vX.Y.0`, besides a tag on main. A dry run from that branch checks the same ancestry, and the latest release and image tags go only to the highest final release. CONTRIBUTING.md has the steps.
