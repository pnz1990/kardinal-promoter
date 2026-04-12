# Item: 033-promote-command

> dependency_mode: merged
> depends_on: 011-cli-foundation, 015-cli-full

## Summary

Add `kardinal promote <pipeline> --env <env>` command. Listed in docs/cli-reference.md and J5 definition-of-done but command stub (`cmd/kardinal/cmd/promote.go`) is missing.

## GitHub Issue

#119 — feat(cli): add kardinal promote command

## Acceptance Criteria

- `kardinal promote <pipeline> --env <env>` creates a Bundle targeting the specific environment
- Command is registered in root.go cobra subcommands
- Output: "Promoting <pipeline> to <env>: bundle <name> created"
- Error if pipeline not found or env not in pipeline spec
- Documented in docs/cli-reference.md

## Files to modify

- `cmd/kardinal/cmd/promote.go` (create)
- `cmd/kardinal/cmd/root.go` (register subcommand)
- `docs/cli-reference.md` (update)

## Size: S (simple CLI command, creates a Bundle)
