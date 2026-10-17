---
section: Development
---

- **Release guards.** A patch tag whose commit already contains a later line's release (a higher minor or major, or its prerelease) is refused (main merged into a maintenance branch, or an old line's patch tagged on main), and release runs queue one at a time, so two cannot race the latest image tags.
