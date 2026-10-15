---
section: Added
---

- **kardinal mcp: the `verify` and `verdicts` tools** (#1845) — an agent connected over MCP can record a verdict (pass, fail or inconclusive) for a check on the version that runs in an environment, with `--allow-write`, and list the verdicts on a version with whether a verify gate counted each ([Connect an agent](agentic/connect-an-agent.md#the-tools)). The version is required, so nothing is recorded when another one runs there; the verdict carries the actor stamp; what a verifier wrote comes back labelled untrusted. The agent's Role needs `create` on `verdicts` for `verify` ([A least-privilege identity](agentic/connect-an-agent.md#a-least-privilege-identity)).
