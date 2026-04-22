# Spec: fix-ci-upgrade-syntax

## Zone 1 — Obligations (falsifiable)
1. `otherness-config.yaml` must have `agent_version: v0.3.0`
2. The `Install otherness agent files` workflow step must pass `bash -n` syntax check
3. The upgrade rewrite of `agent_version` must not use embedded quote escapes inside a
   double-quoted bash string; it must use a heredoc-written temp python script instead
4. Build (`go build ./...`) and vet (`go vet ./...`) must pass unchanged

## Zone 2 — Implementer's judgment
- Minimal change: update version pin + fix quoting in one PR
- No changes to Go code needed

## Zone 3 — Scoped out
- Fixing the root cause in the otherness repo itself (that's an upstream issue)
- Running full test suite (no Go logic changed)

## Design reference
- N/A — infrastructure change with no user-visible behavior
