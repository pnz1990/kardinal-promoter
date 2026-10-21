---
section: Changed
---

- **An agent index that isn't Ready no longer refuses a person** (#2451, [How the actor is verified](actors.md#how-the-actor-is-verified)). The index isn't Ready right after an install or upgrade, while it's full (`IndexFull`), and after a rollback from a release that wrote it in a newer format (`CompactFormat`). Until now, the actor policies refused everyone's checked writes in those states, people's included: versions, Approvals, Verdicts, overrides, freezes and their exemptions, and stamped pauses, hold releases and rejections. During `IndexFull` that meant nobody could ship, freeze or pause until AgentIdentities were trimmed.
  - Now only ServiceAccounts are refused then, with the same messages, and the UI and Bundle APIs answer only them 503.
  - A person is admitted. `kardinal`, the UI and the Bundle API stamp them `kind: unverified`, a new actor kind, and a `human` stamp is refused until the index is Ready again, since it can't be verified.
  - An `unverified` Approval or Verdict doesn't count toward a gate; approve again once the index is Ready.
  - Gates see `actor.kind == "unverified"`, and the UI shows "unverified" beside the name.
  - An agent registered by a User or Group subject passes as `unverified` in that window. It still passes as a person if it writes unstamped records with `kubectl`, and an `unverified` version isn't held as an agent's (see the page for what this can't do).
