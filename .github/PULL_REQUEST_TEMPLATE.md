## What and why

<!-- What changes for users, and why. Link the issue: "Closes #N". -->

## Tests

<!-- Unit tests (table-driven, -race) and the live e2e tests that cover the
change (test/e2e/live), with the coverage.tsv rows they cover. Pull requests
don't run the live suites in CI: run the suites your change touches
(make e2e-up SUITE=<suite>, then make test-e2e-live SUITE=<suite>) and paste
the summary line here. -->

```
# go test ./... -race -count=1

# make test-e2e-live SUITE=<suite>

```

## Checklist

- [ ] `go build ./...`, `go vet ./...` and `go test ./... -race` pass
- [ ] Every new behaviour has a test; a feature has a live e2e test and a `test/e2e/coverage.tsv` row, in id order, citing `path#anchor` (`go test ./test/hack` passes)
- [ ] User docs in `docs/` are updated, and a user-facing change adds `docs/changelog.d/<issue>-<slug>.md` (or has the `no-changelog` label)
- [ ] New `.go` files have the Apache 2.0 header; no `util.go`, `helpers.go` or `common.go`
- [ ] Generated files are regenerated (`make manifests generate api-docs`, `web/dist` with `make ui`, CLI docs with `go run ./hack/gen-cli-docs/main.go`)
- [ ] The title follows Conventional Commits (`feat(scope): ...`, `fix(scope): ...`)
