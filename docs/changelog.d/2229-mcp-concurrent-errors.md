---
section: Fixed
---

- **`kardinal mcp` no longer races between concurrent tool calls** (#2229). A failed call's error was rewritten into its hint (the pipelines in its namespace, the context it could not reach) outside the server's lock, reading the CLI's flag variables while another agent's call had set them to its own namespace: a data race, and a hint that could name the wrong namespace. The rewrite now happens while the call holds the lock, with its own namespace.
