# PR: [work item title]

## Work Item

<!-- Link to the work item this PR implements -->
- Item: `docs/aide/items/<NNN-item-name>.md`
- Spec: `.specify/specs/<feature>/spec.md`
- Design doc: `docs/design/<feature>.md`

## What this implements

<!-- 2-3 sentences describing what was built and why -->

## Acceptance criteria checked

<!-- Paste the acceptance scenarios from spec.md and mark each as passing -->

- [ ] AC-1: Given [...], When [...], Then [...]
- [ ] AC-2: Given [...], When [...], Then [...]

## Tests

```
go test ./... -race
# paste output here
```

## Checklist (agent self-review before opening PR)

- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] All new `.go` files have Apache 2.0 copyright header
- [ ] No `util.go`, `helpers.go`, or `common.go` created
- [ ] Error wrapping uses `fmt.Errorf("context: %w", err)`
- [ ] Every reconciler has at least one idempotency test
- [ ] `/speckit.verify-tasks.run` shows no phantom completions
- [ ] User docs in `docs/` are consistent with implementation
- [ ] Examples in `examples/` still work

## QA notes

<!-- QA agent fills this in during review -->
