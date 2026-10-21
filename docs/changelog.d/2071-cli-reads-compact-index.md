---
section: Added
---

- **The CLI reads the next release's agent index** (#2071, [How the actor is verified](actors.md#how-the-actor-is-verified)). v0.14 writes the AgentIndex in a compact format: each entry is one string in `status.compactUsers` and `status.compactGroups`, and `Ready` is False with the reason `CompactFormat`. This release's `kardinal` CLI and `kardinal mcp` read that format, so they keep stamping agents correctly once the cluster runs v0.14 (N-1 CLI skew). Without this, an agent's CLI would stamp it as a person or a bot, and the policy would refuse it. The controller's UI API, Bundle API and readiness check read the format too. This release never writes it, and its actor policies read only the current format. Admission still decides every write.
