---
section: Fixed
---

- **A failed `kardinal mcp` tool call's error hint names the call's own namespace** (#2229). In v0.12.0 to v0.12.3 a hint was written outside the server's lock, so it could name another agent's namespace (a data race); see `kardinal mcp` runs tool calls concurrently, under Changed (#2230).
