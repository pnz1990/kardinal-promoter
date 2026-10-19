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
an unused file, export or dependency in `web/` (knip, after `npm ci` in `web/`), and a Go package
whose tests can reach code that loads the ambient kubeconfig (client-go's default loading rules,
controller-runtime's `GetConfig`, the in-cluster config) without `TestMain` calling
`kubetest.Main` (`test/kubetest`). That guard points `KUBECONFIG` at a path that does not exist
and unsets `KUBERNETES_SERVICE_HOST` before any test runs, so a unit test can never reach the
cluster your machine points at; a test that needs one gives its kubeconfig explicitly. A helper only
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

### Release gate

Before a release is tagged, `make release-gate` gathers its evidence on one
host and writes `test/e2e/results/release-gate/<commit>/report.md`. The
report has:

- a pass or fail row for each area;
- test totals, and every skipped test by name;
- the coverage proof;
- what the gate does not run, each with the evidence that stands in for
  it;
- durations;
- the commit, the tool versions, and the IDs of the images the live suites
  ran and trivy scanned;
- a link to each step's log.

It covers:

- every live suite, its upgrade suites included, on exactly the oldest
  supported and the newest Kubernetes (the lowest and highest
  `KIND_NODE_1_<minor>` in `hack/tool-versions.env`, now 1.35 and 1.37). A
  suite `hack/e2e/matrix.txt` pins to its minors runs on those only; the
  report lists its other minor as PINNED, with matrix.txt's reason, and that
  does not hold the verdict;
- the upgrade from the previous release;
- the scale suite's `full` and `soak` profiles;
- `go test -race -count=3`, every fuzz target, the scenario matrix and the
  enterprise scenario;
- govulncheck, golangci-lint, `make hygiene`, trivy on the images, the
  strict docs build with check-site-offline, and the bundle size budget;
- a `go build` of every binary `release.yml` ships, for its platform: the
  CLI for linux, darwin and windows, and the controller and render on
  linux/arm64 too;
- helm lint and template of every values profile in
  `hack/release-gate/profiles`, and envtest: every CRD and the chart's
  admission policies on the same two Kubernetes versions.

It does not run what `hack/release-gate/not-run.txt` lists: receipts signed
with a real cloud KMS key, a real EKS cluster, public-good sigstore, real
ServiceNow and Jira, what `release.yml` publishes, and the artifacts for
platforms other than linux/amd64. Each line gives the item, why the gate
does not run it, and the evidence that stands in for it: covered
`test/e2e/coverage.tsv` rows, or `release.yml:<job>`. A line with no reason
or evidence, or one citing a row or job that does not exist, stops the gate
before it runs anything.

The verdict is GO only when everything is proven:

- **A step passes** only when it exits 0 and leaves its log and test output.
  The output must record at least the step's minimum number of tests, with
  no more skips than the step allows. A green CI job with no artifact is not
  a pass.
- **Every covered `test/e2e/coverage.tsv` row** must have run and passed in
  the gate's live suites. A covered row that no test claims anymore (its
  proof result is `todo`) is not proven either. An exception is a line in
  `hack/release-gate/coverage-allow.txt`, with its reason.
- **Missing evidence is NO-GO.** Examples are an upgrade suite from the
  previous release that does not exist, or no node image for a minor.
- **INCOMPLETE** means a step could not run here, such as the github suite
  without a token.

A pass is reused when the gate runs again, so a second run re-runs only what
failed. It is reused only for the same commit, the same inputs (the step's
scripts and thresholds, the options, `hack/tool-versions.env`) and its
evidence still in place. A tree with uncommitted changes reuses nothing, and
its results are marked and never reused. `-fresh` runs everything. Pass the
options in `RELEASE_GATE_ARGS`:

```bash
make release-gate RELEASE_GATE_ARGS='-lock /path/to/LOADLOCK -slots /path/to/CLUSTER-SLOTS -clusters 3'
make release-gate RELEASE_GATE_ARGS='-live ci -lock ...'              # live suites in e2e-live.yml
make release-gate RELEASE_GATE_ARGS='-list'                           # the steps
make release-gate RELEASE_GATE_ARGS='-only ^e2e-core -fuzztime 1m'    # some steps
make release-gate RELEASE_GATE_ARGS='-report'                         # the report only
```

The host rules are on by default:

- **Heavy steps need the lock.** These are the scale profiles, the
  `-race -count=3` run, the enterprise scenario and fuzzing over a minute.
  They need the host's heavy-load lock: `-lock DIR` or `$RELEASE_GATE_LOCK`,
  a directory made with mkdir. Two take it at once while the host's
  one-minute load is under 32, one at or above it, in a fair queue
  that `hack/loadlock.sh` shares: waiters are served in the order they asked,
  and `-priority release` (a release gate; `loadlock.sh -p release`) before
  everyone else. A lock or queued ticket of a process that is gone (another
  boot's, or a reused pid's) is removed, by the head waiter only. The queue
  orders only the clients that use it: a worktree on code from before it
  takes the lock without a ticket, and waiters log when such a holder has
  it; rebase to use main's `hack/loadlock.sh`.
- **Cluster steps need slots.** A step that starts kind clusters needs the
  host's shared slots (`-slots DIR` or `$RELEASE_GATE_SLOTS`), one per
  cluster.
- **Opting out.** On a host of your own, pass `-no-lock` or `-no-slots`.
  Without them, the gate refuses to start.
- **Cluster budget and load.** At most `-clusters` kind clusters run at once,
  and gitlab, multi-cluster and scale count as two. A new cluster starts
  only while the one-minute load is at most `-max-load` (default 64) and
  `-min-mem` GB (default 20) are free.
- **Cleanup.** Interrupting the gate (Ctrl-C) deletes the clusters it
  created; every cluster's name starts with `kp-rg-`.

With `-live ci`, the live suites run as one dispatch of `e2e-live.yml` on
the pushed branch, every suite on both minors (its `minors` input). Each
job's result, log and artifact become a step, and the run's image IDs go
into the report. Running the gate again on the same commit waits for that
run, and re-runs only its failed jobs. The scale profiles stay on the host:
a runner is too small for them.

### Patch releases from a maintenance branch

A patch (`vX.Y.Z`, Z > 0) for a released minor ships from `release/vX.Y` when main already
holds the next minor's work. `release.yml`'s preflight accepts a tag on main, or a patch tag on
`release/vX.Y` whose commit descends from `vX.Y.0`. A `.0` is always tagged on main. The GitHub
release and the `latest` image tags go only to the highest final release, so a v0.12.x released
after v0.13.0 does not take them.

1. **The branch.** `git branch release/vX.Y vX.Y.0` and push it, once per minor.
2. **The fixes.** Cherry-pick each fix's squash commit from main (`git cherry-pick -x`), with
   its `docs/changelog.d` fragment.
3. **The release commit on the branch:**
   - `make changelog-release VERSION=vX.Y.Z` (only the cherry-picked fragments);
   - chart `version: X.Y.Z` / `appVersion: "vX.Y.Z"`;
   - the `--version` pins in the docs and `docs/upgrade.md`.
4. **Evidence.** A dry run from the branch (`gh workflow run release.yml --ref release/vX.Y -f
   version=vX.Y.Z-dryrun.1`): its preflight checks the branch's ancestry and that the version is
   in the branch's line. Then the release gate's patch scope, which the release issue lists.
5. **Tag** the branch head: `git tag -a vX.Y.Z -m 'kardinal-promoter vX.Y.Z'`. Push one release tag
   at a time: release runs share one concurrency group, and a third tag pushed while one runs
   and one waits cancels the waiting one. A release shown as cancelled is re-run from its run
   page (its tag is still there).
6. **Back on main**, one PR:
   - adds the `## [vX.Y.Z]` section to `docs/changelog.md`;
   - deletes the cherry-picked fragments (main's other unreleased fragments stay);
   - bumps the docs pins to vX.Y.Z when it is the newest release.

   The docs site deploys from main.

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
   - Tests for new behavior, with their `test/e2e/coverage.tsv` rows (see Coverage rows)
   - Updated documentation in `docs/`
   - A changelog entry for a change users see (see Changelog)
   - Apache 2.0 license header in new Go files
3. Ensure `go build ./...`, `go test ./... -race`, and `go vet ./...` all pass.
4. Open a pull request against `main`.

A PR from anyone other than the maintainer that changes `AGENTS.md`, `CLAUDE.md`, `.claude/`,
`.github/workflows/` or `.github/actions/` fails the **agent instructions guard** check. Those
files are instructions for coding agents or run with repository secrets, so the maintainer makes
those changes; describe what you need in an issue or in the PR.

PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/):
`feat(scope): description`, `fix(scope): description`, `chore(scope): description`.

### Changelog

Do not edit the `[Unreleased]` section of `docs/changelog.md`: every pull request edited the same
lines there, and nearly every pair conflicted. A change users see adds a file of its own,
`docs/changelog.d/<issue or PR number>-<slug>.md`. It has front matter with its section (Upgrade,
Added, Changed, Deprecated, Removed, Fixed, Security, Documentation or Development) and the entry
as a `- **Title** (#N) — ...` bullet; [docs/changelog.d/README.md](docs/changelog.d/README.md)
has the format. The docs site lists the files under [Unreleased], and
`go run ./hack/changelog unreleased` prints the section.

A pull request that changes `api/`, `cmd/`, `pkg/`, `chart/`, `config/`, `web/src/` or the user docs
needs a file there: the `changelog` check fails without one. Test-only, CI and tooling changes
need none. Label the pull request `no-changelog` when a user-facing change has nothing to tell
users. Before a release is tagged, `make changelog-release VERSION=vX.Y.Z` writes the files into
`docs/changelog.md` as that version and deletes them; then write the release's highlights and
upgrade notes above its sections.

### Coverage rows

`test/e2e/coverage.tsv` is sorted by id: add a row where its id sorts. Cite sources as
`path#anchor`, never `path:N`. The anchor is a Go declaration (`Reconcile`,
`Reconciler.Reconcile`, a type, var or const), a markdown heading, or else text that appears on
exactly one line of the file. A line number moved with every edit above it, so any pull request
that touched a cited file rewrote other rows. `go test ./test/hack -run TestE2ECoverage` checks
that every anchor names one place. [test/e2e/README.md](test/e2e/README.md#coverage) has the
rules.

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
- **Path-scoped pull request CI.** A pull request runs only the CI jobs of the areas it changes
  (`hack/ci/changes.sh`): Go jobs for Go, tests, `hack/`, examples or the chart; the frontend job for
  `web/`; helm lint for the chart; docs-lint for the docs or Go. A docs-only pull request runs
  docs-lint and the checks every pull request runs, and is green in a few minutes. A change to
  `.github/`, `go.mod` or `go.sum` runs everything. The skipped jobs' required checks report as
  passed, and `build` fails when the `changes` job does.
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
