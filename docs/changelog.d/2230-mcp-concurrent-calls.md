---
section: Changed
---

- **`kardinal mcp` runs tool calls concurrently** (#2230). A call no longer takes a server-wide lock to set the CLI's flag variables for its command. The output format, colour and warnings are set once when the server starts. A call's namespace reaches its command as an argument, and the client that connected is recorded in the stamp's asserted fields through the call. An agent's slow call (a large `status` or `history`) no longer holds up another agent's. Error hints list the namespace the call ran in.
