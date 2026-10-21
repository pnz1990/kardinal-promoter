---
section: Upgrade
---

- **Use this release's `kardinal` CLI, and roll back only to v0.13.1 or later** (#2451).
  - While the agent index isn't Ready, the actor policies now require a person's stamp to say `kind: unverified`. An older CLI stamps `human` then, and its write is refused with "actor must be {kind: unverified, …}": upgrade the CLI with the chart, or retry once the index is Ready.
  - While the index isn't Ready, a version, an Approval, a Verdict, a gate override or a freeze exemption written with `kubectl` must carry `actor: {kind: unverified, principal: <you>}`; unstamped, it's refused.
  - v0.13.0 doesn't know the `unverified` kind and would count it as a person's. A rollback from this release must go to v0.13.1 or later, which never counts an actor kind it doesn't know (#2471).
