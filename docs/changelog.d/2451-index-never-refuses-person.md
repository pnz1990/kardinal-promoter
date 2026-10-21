---
section: Changed
---

- **While the agent index isn't Ready, a person is never refused a stop-the-line write; a raw Approval or Verdict must carry the unverified stamp** (#2451, [How the actor is verified](actors.md#how-the-actor-is-verified)). The index isn't Ready right after an install or upgrade, while it's full (`IndexFull`), and after a rollback from a release that wrote it in a newer format (`CompactFormat`). Until now, the actor policies refused everyone's checked writes in those states, people's included: versions, Approvals, Verdicts, overrides, freezes and their exemptions, and stamped pauses, hold releases and rejections. During `IndexFull`, nobody could ship, freeze or pause until AgentIdentities were trimmed.
  - Now only ServiceAccounts are refused those writes, with the same messages, and the UI and Bundle APIs answer only them 503.
  - A person's version, freeze, exemption, override, pause, hold release or rejection goes through. `kardinal`, the UI and the Bundle API stamp it `kind: unverified`, a new actor kind, and a `human` stamp is refused until the index is Ready again, since it can't be verified.
  - A raw Approval or Verdict must carry the unverified stamp: `kardinal approve`, the UI and `kardinal verify` set it; with `kubectl`, set `spec.actor: {kind: unverified, principal: <you>}`. An `unverified` Approval or Verdict doesn't count toward a gate, so decide again once the index is Ready.
  - Gates see `actor.kind == "unverified"`, and the UI shows "unverified" beside the name.
  - An agent registered by a User or Group subject passes as unverified in that window, and a version it ships then isn't held as an agent's (see the page for what this can't do).
