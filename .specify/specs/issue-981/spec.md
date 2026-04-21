# Spec: No `kardinal completion` works for all shells

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future`
- **Implements**: No `kardinal completion` works for all shells (🔲 → ✅) + `kardinal completion` CI test is absent (🔲 → ✅)

---

## Zone 1 — Obligations

**O1 — `TestCompletion_Bash` must verify that `kardinal completion bash` output is non-empty
and contains `__start_kardinal`.**

The bash completion script is dynamic (cobra V2). Verifying the entry-point function name
confirms the script structure is correct without requiring static command embedding.

**O2 — `TestCompletion_Zsh` must verify that `kardinal completion zsh` output is non-empty
and contains `_kardinal`.**

The zsh completion script is also dynamic. Verifying the function name confirms the script
structure. Command names are resolved at runtime via `__complete` — not embedded in the script.

**O3 — `TestCompletion_CoreSubcommandsComplete` must verify that all core subcommands
(`get`, `explain`, `logs`, `status`, `rollback`, `approve`) appear in `__complete ""` output.**

This test catches command tree mis-wiring (a command added to a file but not wired via
`AddCommand` in `root.go`) that static script inspection cannot catch. The `__complete`
protocol is what tab-completion actually uses at runtime.

**O4 — All existing completion tests must continue to pass (fish, powershell, unknown shell, no-arg, help).**

No regressions. The additions are additive.

---

## Zone 2 — Implementer's judgment

- The `coreSubcommands` slice is defined at the package level so all test functions
  can reference it. The choice of `[]string{"get", "explain", "logs", "status", "rollback", "approve"}`
  covers the most frequently used commands. More can be added as needed.
- The `__complete ""` invocation (single empty string arg) is the correct cobra protocol
  for getting top-level completions. `__complete "" ""` (two args) produces "unable to find
  a command" errors. This was verified empirically.
- The `_ = root.Execute()` pattern suppresses the exit-code side effect while still
  capturing output — appropriate for `__complete` which exits non-zero on no results.

---

## Zone 3 — Scoped out

- This spec does NOT add fish/powershell subcommand verification tests (low priority for
  platform engineers; bash/zsh coverage is sufficient for the design doc item).
- This spec does NOT test completion of sub-subcommands (e.g. `get pipelines`, `create bundle`)
  — that is a follow-up if needed.
- This spec does NOT add integration tests that actually load the completion script in a
  live shell and trigger tab events — that requires shell process spawning beyond Go test scope.
