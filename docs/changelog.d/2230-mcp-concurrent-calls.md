---
section: Changed
---

- **`kardinal mcp` runs tool calls concurrently** (#2230). A call no longer takes a server-wide lock to set the CLI's flag variables for its command. A call's namespace reaches its command as an argument, and its output format, colour, warnings and caller, and the client that connected (recorded in the stamp's asserted fields), come with the call. An agent's slow call (a large `status` or `history`) no longer holds up another agent's. Error hints list the namespace the call ran in: before, a failed call's hint was written outside the lock and could name the namespace of another agent's call (#2229).
