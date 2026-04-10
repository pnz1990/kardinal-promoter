# Item 004: GitHub Actions CI + golangci-lint Config

> **Queue**: queue-001 (Stage 0)
> **Branch**: `004-ci-and-lint`
> **Depends on**: 001-go-module-scaffold (must be `done` in state.json)
> **Assignable**: after item 001 merged to main
> **Contributes to**: All journeys (CI gate enforcement)

---

## Goal

Deliver the GitHub Actions CI workflow that runs on every push to `main` and every PR.
Also configure `golangci-lint` with the project's linter rules so that QA checklist
items ("CI lint passes") have something to check against.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 0 (GitHub Actions CI workflow + .golangci.yml deliverables)

---

## Deliverables

1. `.github/workflows/ci.yml`:
   ```yaml
   name: CI
   on:
     push:
       branches: [main]
     pull_request:
       branches: [main]
   jobs:
     build:
       runs-on: ubuntu-latest
       steps:
         - uses: actions/checkout@v4
         - uses: actions/setup-go@v5
           with:
             go-version: "1.23"
             cache: true
         - run: go build ./...
         - run: go vet ./...
         - run: go test ./... -race -count=1 -timeout 120s
         - uses: golangci/golangci-lint-action@v6
           with:
             version: v1.61
   ```
2. `.golangci.yml` with baseline linter config:
   - Enabled linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`,
     `gofmt`, `goimports`, `misspell`, `unconvert`, `gocritic`
   - Disabled: `exhaustive`, `wrapcheck` (too noisy for early dev)
   - `linters-settings.goimports.local-prefixes: github.com/kardinal-promoter`
   - `issues.exclude-rules`: exclude `_test.go` from `errcheck`
   - `run.timeout: 5m`
3. `.github/CODEOWNERS`:
   ```
   * @pnz1990
   ```
4. `.github/PULL_REQUEST_TEMPLATE.md`: standard PR template referencing
   `docs/aide/pr-template.md` for required sections
5. `.github/ISSUE_TEMPLATE/` directory with:
   - `bug_report.md`: standard bug template
   - `feature_request.md`: standard feature request template
6. `.github/dependabot.yml`:
   ```yaml
   version: 2
   updates:
     - package-ecosystem: "gomod"
       directory: "/"
       schedule:
         interval: "weekly"
     - package-ecosystem: "github-actions"
       directory: "/"
       schedule:
         interval: "weekly"
   ```
7. `Makefile` `lint` target updated: `golangci-lint run ./...`

---

## Acceptance Criteria

- [ ] `.github/workflows/ci.yml` is valid YAML (`yamllint` or `actionlint` passes)
- [ ] `golangci-lint run ./...` passes with zero errors on the current codebase
- [ ] `go vet ./...` passes (included in CI)
- [ ] `.golangci.yml` enables at minimum: `errcheck`, `govet`, `staticcheck`
- [ ] `.github/CODEOWNERS` assigns `@pnz1990` to all files
- [ ] No banned filenames introduced by this item

---

## Self-Validation Commands

```bash
golangci-lint run ./...
go vet ./...
yamllint .github/workflows/ci.yml  # or: actionlint .github/workflows/ci.yml
```

---

## Journey Contribution

CI gate is checked by QA for every PR:
```
□ CI lint passes (PROJECT.lint_command)
```
This item establishes that gate. Without it, QA cannot approve any PR.
