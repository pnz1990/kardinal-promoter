# PR: [item title]

## Item Reference

- **Item**: `docs/aide/items/<NNN-item-name>.md`
- **Spec**: `.specify/specs/<feature>/spec.md`
- **Design doc**: `docs/design/<feature>.md`

## What this implements

<!-- 2-3 sentences: what was built and why -->

## Acceptance Criteria

<!-- Copy every Given/When/Then from spec.md and mark each -->

- [ ] Given [...], When [...], Then [...]
- [ ] Given [...], When [...], Then [...]

## Test Output

```
# go test ./... -race (paste output)

```

## Phantom Completion Check

```
# Verify all [X] tasks have real implementation (paste output)

```

## Manual Validation

```
# kubectl apply -f examples/quickstart/ (or multi-cluster-fleet/)
# paste the full terminal output here

```

**Behavior matches docs/**:
- [ ] `docs/quickstart.md` step X works as documented
- [ ] `docs/concepts.md` behavior X is correct

## Pre-merge Checklist (engineer completes before requesting review)

- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` zero findings
- [ ] All [X] tasks have real implementation (no phantom completions)
- [ ] All acceptance criteria from spec.md implemented
- [ ] Manual kubectl validation output included above
- [ ] All new `.go` files have Apache 2.0 copyright header
- [ ] No `util.go`, `helpers.go`, `common.go` created
- [ ] No new entry in `go.mod require` block (or needs-human label set)
- [ ] Every new reconciler has an idempotency test
- [ ] No kro module import

## QA Notes

<!-- QA agent fills this section during review -->
