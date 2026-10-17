---
section: Deprecated
---

- **`kardinal explain`** (#1758) — use `kardinal why <pipeline> [<env>] -o wide`, which prints the same table. `-o json` and `-o yaml` print the same entries in both. `explain` keeps working with every flag it took (`--env`, `-w`, `--color`, `-o json|yaml`) and prints the same output, but now also writes a one-line notice on stderr that names the `why` command to use instead. It no longer appears in the CLI help or in shell completion. Its reference page redirects to [kardinal why](reference/cli/kardinal-why.md). It will be removed in a later release. The docs now use `kardinal why`. The hints the CLI prints use it too: `rollback` prints `Track with: kardinal why <pipeline> <env>`, and the `why` answer for a held environment suggests `kardinal why <pipeline> <env> -o wide`. The MCP server's `explain` tool keeps its name.
