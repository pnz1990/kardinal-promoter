---
section: Added
---

- **kardinal mcp: the `freeze` and `freezes` tools, and `simulate` as an agent** (#1845, [Connect an agent](agentic/connect-an-agent.md#freezes)). With `--allow-write`, an agent can freeze agents or stop the line for everyone, in every namespace or one, with a reason. The `freezes` tool lists the freezes, with their reasons labelled untrusted. No tool lifts a freeze or exempts a pipeline from one: a person does that. The `simulate` tool takes `asAgent` to evaluate a version that agent created, against its `agents:` setting and `actor.*` and `agent.*`.
