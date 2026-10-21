---
section: Changed
---

- **While the agent index isn't Ready, stopping is always allowed and going needs verification** (#2451, [While the index isn't Ready](actors.md#while-the-index-isnt-ready)). The index isn't Ready for a few seconds after a fresh install, while it's full (`IndexFull`), and after a rollback from a release that wrote it in a newer format (`CompactFormat`). Until now, the actor policies refused everyone's checked writes in those states, people's included: during `IndexFull`, nobody could freeze, pause or ship until AgentIdentities were trimmed.
  - Only ServiceAccounts are refused now, with the same messages, and the UI and Bundle APIs answer only them 503.
  - A person's write goes through stamped `kind: unverified`, a new actor kind, which counts as an agent for every restriction and as nobody for every grant. `kardinal`, the UI and the Bundle API set it; a `human` stamp is refused until the index is Ready again.
  - Stopping works: a freeze, a pause, a hold, a rejection.
  - Going waits for the index: an `unverified` version is held like an agent's (an agents-only freeze holds it, and its agent gates hold it, saying why); an `unverified` Approval or Verdict doesn't count; an `unverified` gate override or freeze exemption is recorded but not honoured. Unstamped, these writes are refused, and the refusal gives the fix.
  - Gates see an `unverified` version as `actor.kind == "agent"`, with the new `actor.unverified` true and `actor.verified` false. Once the index is Ready and names its writer as an agent by a User subject, it is that agent's, and the one-party rule sees its sponsor. The UI shows "unverified" beside the name.
  - The cost: during `IndexFull`, a break-glass override waits for the index to be fixed. Watch `NearFull` and the `KardinalAgentIndexNearFull` alert, and trim early (the page has the steps).
