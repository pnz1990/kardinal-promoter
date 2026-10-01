# Live e2e tests

`test/e2e/live` runs kardinal against real components on a kind cluster: a
real git server, a real GitOps engine, real workloads, and the controller and
CLI built from the checkout. Nothing is faked.

## Run a suite

```bash
make e2e-up SUITE=core        # kind cluster kardinal-e2e-core + components (~5 min cold)
make test-e2e-live SUITE=core # the suite's tests
make e2e-down SUITE=core
```

`make e2e-up` is idempotent: re-run it after changing the controller to
rebuild and redeploy it. It writes `test/e2e/results/kardinal-e2e-<suite>/env`
(git server URLs and tokens, mode 0600), which `make test-e2e-live` sources.
`make e2e-down SUITE=multi-cluster` deletes its spoke cluster too.

On hosts where `docker build` can't download Go modules, set
`KARDINAL_E2E_BUILD=host` to build the controller binary on the host instead.

`make test-e2e-live` fails when a test fails or skips, or when no test ran,
and keeps the `go test -json` stream in `test/e2e/results/<cluster>/test.json`.
`COUNT=3` repeats every test, `SHARD=2/3` runs every third of the suite's
tests starting at the second. `make e2e-up KIND_K8S=1.37` picks the
Kubernetes minor (one of the `KIND_NODE_*` images in `hack/tool-versions.env`;
the default is `test/e2e/kind-config.yaml`'s). Use the kind version pinned
there: older kind releases can't boot its node images. CI also installs the
helm version pinned there. The core suite also
needs bash and zsh on PATH: `TestCLI_Completion` checks the completion scripts
with `bash -n` and `zsh -n`.

## Run every suite

```bash
make e2e-all                       # hack/e2e/all.sh: every job in hack/e2e/matrix.txt
make e2e-all JOBS=8                # clusters at once (default 4)
SUITES='gitea flux' make e2e-all   # only these suites
```

`hack/e2e/matrix.txt` lists the jobs: every suite, the core suite on each of
the three Kubernetes minors split across two jobs per minor with `SHARD`, and
the upgrade suite on Kubernetes 1.29 and the newest minor.
`make e2e-all` runs them on this host, each on a kind cluster of its own
(`kardinal-e2e-all-<job>`), deletes the clusters (`KEEP=1` keeps them), and
runs `go run ./test/e2e/proof` over all the results. It fails when a job
fails, or when a covered row's test failed, skipped or did not run. Each
cluster takes 2-3 GB of memory, GitLab's about 7 GB. The results, each
job's log and the proof are in `test/e2e/results/all-<UTC time>/`. The
`github` job runs only when `KARDINAL_E2E_GITHUB_TOKEN_FILE` or
`DEMO_GITHUB_TOKEN` is set; otherwise it is listed as not run. The upgrade
jobs run once whatever `COUNT` is: the test upgrades its cluster
(`hack/e2e/run.sh` refuses `COUNT` above 1 for it).

Pull requests don't run the live suites in CI: run `make e2e-all`, or the
suites a change touches, before you merge, and put the result in the PR.
`.github/workflows/e2e-live.yml` runs the same jobs weekly, repeating every
test three times to find flakes (the upgrade test once), and when dispatched
(`gh workflow run e2e-live.yml --ref <branch>`; `-f count=N` repeats each
test). Only its `github` job gets the `DEMO_GITHUB_TOKEN` secret. Its
`e2e live` job passes when every job passed and the coverage proof holds.

## Suites

`hack/e2e/up.sh` defines the suites: each one's components and the `RUN`
pattern of its tests.

| Suite | Components | Tests |
|---|---|---|
| `core` | Forgejo, Argo CD, a webhook receiver for NotificationHooks, two OCI registries for Subscriptions, the Bundle API token | `TestCore_*`, `TestSCM_*`, `TestForgejo_*`, `TestGate_*`, `TestBundle_*`, `TestPipeline_*`, `TestGraph_*`, `TestStep_*`, `TestRollback_*`, `TestHealth_*`, `TestCLI_*`, `TestCIAPI_*`, `TestNotify_*`, `TestSub_*`, `TestAudit_*` |
| `gitea` | Gitea, Argo CD | `TestCore_*`, `TestSCM_*`, `TestGitea_*` |
| `gitlab` | GitLab CE, Argo CD | `TestCore_*`, `TestSCM_*`, `TestGitLab_*` |
| `github` | GitHub (branches of `pnz1990/kardinal-demo`), Argo CD, the webhook receiver (the other API host `TestGitHub_SCMAPIURL` points the controller at) | `TestCore_*`, `TestSCM_*`, `TestGitHub_*` |
| `delivery` | Forgejo, Argo CD, Argo Rollouts, Flagger | `TestRollouts_*`, `TestFlagger_*`, `TestDelivery_*` |
| `ui` | Forgejo, Argo CD, four more chart releases (static token and CORS, TokenReview, TokenReview without its RBAC, TLS), Playwright's Chromium | `TestUI_*` |
| `flux` | Forgejo, Flux, Prometheus Operator, Prometheus, Pushgateway, Grafana | `TestFlux_*`, `TestMetric_*`, `TestObs_*` |
| `chart` | Forgejo, Argo CD, cert-manager; no controller release: each test installs its own | `TestChart_*`, `TestDeprecated_*` |
| `upgrade` | Forgejo, Argo CD, kardinal-promoter v0.8.1 with its bundled Graph controller and no kro; the test follows the upgrade guide, so a cluster serves one run. `KIND_K8S=1.29` runs it on Kubernetes 1.29 | `TestUpgrade_*` |
| `multi-cluster` | Forgejo, Argo CD, Flux and Argo Rollouts in the hub, and a second kind cluster (`<cluster>-spoke`, Argo Rollouts) registered with the hub's Argo CD and Flux | `TestMultiCluster_*` |

`TestSCM_*` tests use only `Env.Git`, so they run against every git server;
a test that needs one provider is named after it and checks `Env.Git.Kind()`
first. The `github` suite takes its token from `KARDINAL_E2E_GITHUB_TOKEN_FILE`,
`DEMO_GITHUB_TOKEN` or `gh auth token` (`hack/e2e/components/github.sh`).

> **Warning: the `gh auth token` fallback hands your own GitHub login to the
> cluster.** When neither variable is set, `github.sh` takes the token of
> your `gh` login, with every scope that login has (often `repo`, `workflow`
> and `read:org`, on every repo you can reach). It copies the token into the
> kind cluster, as the controller's Secret `kardinal-system/git-token` and
> a copy of it in every test namespace, and writes it to the suite's env file
> and `secrets/` directory under `test/e2e/results/<cluster>/`. Anyone who can
> read those Secrets or files can act as you on GitHub. Instead, create a
> fine-grained token for the test repo only, with Contents and Pull requests
> read and write, and pass it in `DEMO_GITHUB_TOKEN` (or a file named by
> `KARDINAL_E2E_GITHUB_TOKEN_FILE`).

## Coverage

`test/e2e/coverage.tsv` lists every documented behavior with an id, and
whether a test covers it yet. A test claims rows with one sentence in its doc
comment, `Covers STEP-AUTO-01, SCM-CLOSED-01.`, and only for what it fully
asserts. `go test ./test/hack -run TestE2ECoverage` fails when the file and
the tests disagree, when a live test claims no row, and when no suite runs a
live test. Only live tests cover live rows; contract rows are covered by unit
tests: Bitbucket and Azure DevOps, which can't be self-hosted, and behaviors
a live test cannot force, such as a race between two status writes.

Each suite run writes `test/e2e/results/<cluster>/summary.json`. The `e2e
live` job runs `go run ./test/e2e/proof` on every suite's summary: it fails
when a claimed row's test failed, skipped or didn't run, and the job summary
lists every row's result. `-complete` also fails on rows still todo.

## Rules

- **A live test never skips.** A missing cluster, component or credential
  fails the test. CI treats a skipped test as a failure too.
- **Each test owns its state.** `Env.Namespace` gives the test its own
  namespace and `Env.Repo` its own repo (a branch of one shared repo on
  GitHub), so tests run in any order and never see each other's PRs.
- **Wait on conditions, not time.** Use `framework.Eventually`,
  `WaitStepState` (fails fast when a step reaches a different terminal
  state) and `Consistently` for "must not happen" checks. No `time.Sleep`.
- **Assert what users see.** The Deployment runs the new image, the file in
  git has the new tag, the PR has its labels and evidence, the Bundle ends
  Verified. A PromotionStep state alone is not enough.
- **Gates are checked both ways.** Show the promotion blocked while the gate
  is closed, then allowed once it opens.
- **On failure** the test writes the namespace's kardinal objects, events,
  pod logs and the controller log to
  `test/e2e/results/kardinal-e2e-<suite>/diagnostics/<namespace>/`. The
  namespace is `e2e-<test name>-<hash>`, new on every run.
  `KARDINAL_E2E_KEEP=1` keeps namespaces and repos for debugging.

## Test app

Fixtures deploy [podinfo](https://github.com/stefanprodan/podinfo) at pinned
real tags (`fixtures.V1`..`V3`). It has a readiness probe and Prometheus
metrics, so the same app drives health, canary and MetricCheck tests.
`fixtures.BrokenTag` does not exist, which gives a rollout that never
becomes Available.
