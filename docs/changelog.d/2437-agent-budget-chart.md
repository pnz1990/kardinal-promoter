---
section: Added
---

- **Agent budgets in the chart** (#2437, W6 of [agent budgets](design/24-agent-budgets.md)): `actors.budgets.enabled` (default `false`) turns enforcement on, and `actors.budgets.ledger` names the ledger (default the release; every shard of one installation sets the default shard's). New admission policies apply whether budgets are on or off:
  - `<release>-agent-budgets`: changing an AgentIdentity beyond its exemptions needs the `manage` verb, which `<release>-admin` gains; adding or removing an exemption needs `exempt`, which the new `<release>-agent-budget-exempter` ClusterRole grants. A new exemption must name its maker (`createdBy`), carry their actor stamp, and not be made by the agent's sponsor or one of its subjects. An existing one never changes.
  - `<release>-actor-budgets`: an agent's exemption is refused. It checks only identities labelled `kardinal.io/budget-exemptions: "true"`, which `<release>-agent-budgets` requires exactly while an identity has exemptions, so registering an agent never waits for the agent index.

  While budgets are off, a write that sets budgets is admitted with a warning. The controller counts a stamped exemption only while both policies are installed and bound, and `--agent-budgets` refuses to start without `--agent-budget-exemption-policies`, which the chart sets.
