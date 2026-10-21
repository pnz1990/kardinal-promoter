---
section: Upgrade
---

- **List the people who verify through a verify gate that lists only agents** (#2411). From this release, a verify gate with `verify.allowedAgents` and no `allowedUsers` or `allowedGroups` counts no person's verdict. Before you upgrade, run `kardinal doctor` with the new CLI. Its "Verify gates of agents only" check names those gates, and marks the ones where a person's verdict counted for a version still kept. To keep counting those people, list them in `allowedUsers` or `allowedGroups` (the fields exist in v0.12). A version in flight whose gate counted a person's pass waits after the upgrade until an allowed verifier passes it ([Upgrading from v0.12.0](upgrade.md#upgrading-from-v0120)).
