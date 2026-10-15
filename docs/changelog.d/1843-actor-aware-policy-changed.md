---
section: Changed
---

- **Overrides by agents are not honoured** (#1843) — an override whose actor is an agent never opens a gate; it is named in `OverrideIgnored` and audited with outcome `Failure` ([Overrides](overrides.md)).
- **`inbox`, `why` and `status` count approvals per party** (#1843) — `kardinal inbox`, `kardinal why` and `kardinal status` count approvals per party and use the version's actor stamp for `excludeAuthor`; a gate whose `approval.requiredWhen` is false is not listed as waiting for an approval.
