---
section: Upgrade
---

- **Use this release's `kardinal` CLI** (#2451). While the agent index isn't Ready, the actor policies now require a person's stamp to say `kind: unverified`. An older CLI stamps `human` then, and its write is refused with "actor must be {kind: unverified, …}": upgrade the CLI with the chart, or retry once the index is Ready.
