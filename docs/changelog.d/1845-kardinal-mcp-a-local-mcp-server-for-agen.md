---
section: Added
---

- **`kardinal mcp`, a local MCP server for agents** ([Connect an agent](agentic/connect-an-agent.md), #1845) — the CLI serves its commands as Model Context Protocol tools over stdio, for Claude Code, Codex, Cursor and other agents. It runs with the caller's kubeconfig and context, so the agent has exactly that identity's RBAC; it holds no token and opens no port. The read tools (`why`, `status`, `inbox`, `history`, `diff`, `explain`, `gates`, `simulate`) are on; the write tools (`ship`, `promote`, `rollback`, `pause`, `resume`, `reject`, `approve`) only with `--allow-write`, and a call to one on a read-only server is refused. Text kardinal did not write (PR URLs, step messages from the SCM, a version's author, reasons people typed) comes as `{"untrusted": "..."}`; RBAC denials are tool errors of kind `Forbidden` naming the role to ask for; long lists come in pages. The page has a least-privilege Role for an agent's ServiceAccount.
