# Contributing to kardinal-promoter

Thank you for your interest in contributing to kardinal-promoter!

## Getting Started

### Prerequisites

- Go 1.26+ (the exact toolchain is pinned in `go.mod`)
- `kubectl` and a Kubernetes cluster (kind recommended for local development)
- `helm` for chart-related changes

### Build and Test

```bash
# Build all binaries
go build ./...

# Run all tests (with race detector; CI splits them into shards, see Merging)
go test ./... -race -count=1 -timeout 120s

# Lint
go vet ./...

# No dead code or dead files (Go, docs pages and assets, scripts, web)
make hygiene
```

`make hygiene` fails on a Go function no main or test reaches, a docs page that is neither in
`mkdocs.yml` nor linked, a docs asset or a script under `hack/` or `scripts/` that nothing names,
and an unused file, export or dependency in `web/` (knip, after `npm ci` in `web/`). A helper only
tests call belongs in an `export_test.go` of its package. Intentional exceptions go in
`hack/hygiene/allow.txt` with a reason.

Fuzz targets check the properties of the Graph builder (`FuzzBuild`, every
Graph passes kro's compiler rules), the PolicyGate CEL context
(`FuzzCELContext`) and the YAML editors (`FuzzYAMLUpdate`,
`FuzzKustomizeSetImage`). `go test` runs their seed corpora (`testdata/fuzz/`)
and a deterministic twin of each (`TestBuild_Property` and so on); CI's `fuzz`
job explores for 20s each. To fuzz one for longer:

```bash
go test ./pkg/graph -run '^$' -fuzz '^FuzzBuild$' -fuzztime 5m
```

A failing input is written to the package's `testdata/fuzz/<target>/`: commit
it with the fix.

The embedded UI in `web/` uses npm and `web/package-lock.json` only. The controller
embeds the committed `web/dist`, so after changing `web/` run `make ui` and commit
`web/dist`; CI fails when it differs from a fresh build.

```bash
cd web && npm ci && npm run typecheck && npm test && npm run build && npm run size
```

`npm run size` (CI runs it too) holds the UI to its gzip budgets: **initial JS ≤ 152 KB** (the
import graph from the entry module, its static imports followed transitively, plus the first views
a route needs: the fleet board on the first page, the map on a pipeline, the map and the Table on a
narrow screen, the map and the stage drawer on an `/env/` link; the largest of those loads
counts), **total JS
≤ 200 KB** (every chunk, the lazily loaded views included), the same pair for CSS (initial
≤ 18 KB, the stylesheets of that load; total ≤ 25 KB) and fonts ≤ 180 KB. A
view is loaded with `React.lazy` when only some routes paint it (the fleet board, the map, the stage
drawer, the Matrix, the comparison panel, the command palette, the dialogs, the Activity and
Metrics tabs, the operations table): it counts toward the total, and toward the initial load only
of the routes that paint it first. Its place shows the design system's `Skeleton` while it loads,
sized as the view, so nothing moves (journey 024 measures the layout shift).

The budgets are re-baselined once per release, in `web/scripts/check-size.mjs` (the numbers above
are the v0.11 baseline); between releases they do not move. Every UI PR states its size delta in
its description: the five lines `npm run size` prints on `main` and on the branch.

The `@perf` Playwright journeys (`npm run test:e2e` runs them after the others, on one worker)
scroll the 200-version Matrix diagonally. On every run, CI included, they assert what the page
controls: the cells drawn in any frame (under 600 in the default view, under 1,200 with every
wave and fleet expanded, of 60,200) and no long task over 200 ms. A shared runner cannot hold a
frame budget, so the frame-time p95 (20 ms by default, 33 ms expanded) and the 100 ms long-task
bound are asserted only with `KARDINAL_PERF_STRICT=1`: the UI perf workflow (`.github/workflows/ui-perf.yml`) runs that
nightly and on a dispatch (`gh workflow run ui-perf.yml --ref <branch>`), and locally
`cd web && KARDINAL_PERF_STRICT=1 npx playwright test --grep @perf --workers=1` on an idle machine.

### Run Locally

```bash
# Create the kind cluster kardinal-e2e-core: kro, Forgejo, Argo CD and the
# controller and CLI built from this checkout
make e2e-up SUITE=core

# Run the core live suite against it, then delete the cluster
make test-e2e-live SUITE=core
make e2e-down SUITE=core
```

`make e2e-up` wraps `hack/e2e/up.sh SUITE` and `make test-e2e-live` wraps
`hack/e2e/run.sh SUITE`. `make e2e-all` runs every suite, each on a kind
cluster of its own, JOBS (default 4) at a time, and fails when a test
failed or a coverage row's test did not run. Pull requests don't run the
live suites in CI, so run the ones your change touches and put the result in
the PR. The scripts download the kind, kubectl and helm versions CI uses
into `bin/e2e` (`make e2e-tools`). [test/e2e/README.md](test/e2e/README.md)
lists the suites.

## How to Contribute

### Bug Reports

Open a [GitHub Issue](https://github.com/pnz1990/kardinal-promoter/issues/new/choose)
using the **Bug Report** template. Include:
- The exact commands you ran
- The expected vs. actual behavior
- Your Kubernetes version and how you installed kardinal-promoter

### Feature Requests

Open a [GitHub Issue](https://github.com/pnz1990/kardinal-promoter/issues/new/choose)
using the **Feature Request** template. Describe the problem you are solving, not just
the solution you have in mind.

### Pull Requests

1. Fork the repository and create a branch from `main`.
2. Make your changes. Every PR must include:
   - Tests for new behavior
   - Updated documentation in `docs/`
   - Apache 2.0 license header in new Go files
3. Ensure `go build ./...`, `go test ./... -race`, and `go vet ./...` all pass.
4. Open a pull request against `main`.

A PR from anyone other than the maintainer that changes `AGENTS.md`, `CLAUDE.md`, `.claude/`,
`.github/workflows/` or `.github/actions/` fails the **agent instructions guard** check. Those
files are instructions for coding agents or run with repository secrets, so the maintainer makes
those changes; describe what you need in an issue or in the PR.

PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/):
`feat(scope): description`, `fix(scope): description`, `chore(scope): description`.

### Merging

Every change reaches `main` through a squash-merged pull request; nothing is pushed to `main`
directly. Branch protection requires the CI checks (`build`, `integration tests`,
`golangci-lint`, `frontend build`, `helm lint`, `docs-lint`, `go.sum integrity`,
`e2e stubs compile`, `action-tests`, `agent instructions guard`), but not that the branch be up to
date with `main`. A pull request merges when its own head is green and it has no conflicts.

- **Auto-merge.** Once CI is green on a pull request that is not risky, turn on auto-merge:
  `gh pr merge <n> --squash --auto --delete-branch`. GitHub merges it when the required checks
  pass. There is no merge queue (the repository is user-owned), so do not wait for or rebase onto
  every new `main` commit.
- **Risky pull requests do not auto-merge.** A pull request is risky when it changes `go.mod` or
  `go.sum`, the test infrastructure (`test/hack/`, `test/e2e/framework/`, `hack/`, the
  workflows in `.github/`), a CRD schema (`api/`, `config/crd/`), or a package that another
  ready pull request also changes. A maintainer merges it by hand, after rebasing it onto the
  current `main` and seeing CI green there; several risky pull requests go together on one
  train branch with one CI run.
- **CI on `main`.** Every merge runs CI on `main`, and a run on `main` is never cancelled by the
  next merge (pull request runs are: a new push cancels the old run). `build` passes when the
  build checks (tidy, vet, build, license headers, CRD schemas) and every `go test -race`
  shard pass; the shards split the packages by `hack/ci/test-durations.txt`
  (`go run ./hack/citool shard I/N`). Main restores a Go build cache it saves once a day, so the
  pull request critical path stays under five minutes.

### When main is red

The **red main** workflow (`.github/workflows/red-main.yml`, logic in `hack/ci/red-main.sh`)
runs after every CI run on `main`. On a first failure it re-runs the failed jobs once. When the
run still fails, it opens one issue labelled `red-main` (or updates it) with the failing jobs and
tests and the merges since the last green `main` run, and closes it on the next green run.

Main is red for at most 30 minutes:

1. **Fix forward** when the cause is clear and the fix is small: open the fix as a pull request
   and auto-merge it.
2. **Otherwise revert** the merge that broke `main` (`git revert <sha>` in a pull request, or the
   **Revert** button on the merged pull request) and reopen the original change with the fix.

Until `main` is green, merge nothing but the fix or the revert: every merge on a red `main` makes
the next failure harder to attribute.

### Flaky tests

A test that fails and then passes on the same commit is flaky, and a flaky test is a bug. The
weekly **CI health** workflow (`.github/workflows/ci-health.yml`) keeps one issue labelled
`flaky-test` with every test that failed and passed on the same commit in the last week of CI runs
(a re-run, or two runs of one commit). Fix the test, or skip it with `t.Skip` and a link to its
issue; do not re-run a pull request until it passes without saying so in the pull request. The
same workflow re-measures the test shards and, when `hack/ci/test-durations.txt` splits them
more than 15% off, pushes a refreshed file to the `bot/test-durations` branch and opens an issue
labelled `test-durations` to merge it.

## Code Standards

- Wrap errors with context: `fmt.Errorf("context: %w", err)`. Use `errors.New` only for package-level sentinel errors (`var ErrX = errors.New(...)`) and permanent step failures
- Use `zerolog.Ctx(ctx)` for logging — no `fmt.Println`
- Table-driven tests with `testify/assert` and `require`
- No `util.go`, `helpers.go`, or `common.go` filenames
- Apache 2.0 copyright header in every new `.go` file:

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```

## Community

Questions, bugs and ideas: open a [GitHub Issue](https://github.com/pnz1990/kardinal-promoter/issues)
(label `question` for questions).

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE).
