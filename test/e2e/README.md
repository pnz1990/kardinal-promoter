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

On hosts where `docker build` can't download Go modules, set
`KARDINAL_E2E_BUILD=host` to build the controller binary on the host instead.

`make test-e2e-live` fails when a test fails or skips, or when no test ran,
and keeps the `go test -json` stream in `test/e2e/results/<cluster>/test.json`.
`COUNT=3` repeats every test, `SHARD=2/3` runs every third of the suite's
tests starting at the second. `make e2e-up KIND_K8S=1.37` picks the
Kubernetes minor (one of the `KIND_NODE_*` images in `hack/tool-versions.env`;
the default is `test/e2e/kind-config.yaml`'s). Use the kind version pinned
there: older kind releases can't boot its node images. The core suite also
needs bash and zsh on PATH: `TestCLI_Completion` checks the completion scripts
with `bash -n` and `zsh -n`.

CI (`.github/workflows/e2e-live.yml`) runs every suite but `github` on every
pull request, the core suite on each of the three Kubernetes minors, split
across two jobs per minor with `SHARD`. The `github` suite needs the
`DEMO_GITHUB_TOKEN` secret, so it runs only on pushes to main, the weekly run
and manual dispatches. The weekly run repeats every test three times to find
flakes. The `e2e live` check passes when every suite that ran passed.

`hack/e2e/up.sh` defines the suites: each one's components and the `RUN`
pattern of its tests.

| Suite | Components | Tests |
|---|---|---|
| `core` | Forgejo, Argo CD, a webhook receiver for NotificationHooks, two OCI registries for Subscriptions, the Bundle API token | `TestCore_*`, `TestSCM_*`, `TestForgejo_*`, `TestGate_*`, `TestBundle_*`, `TestPipeline_*`, `TestGraph_*`, `TestStep_*`, `TestRollback_*`, `TestHealth_*`, `TestCLI_*`, `TestCIAPI_*`, `TestNotify_*`, `TestSub_*`, `TestAudit_*` |
| `gitea` | Gitea, Argo CD | `TestCore_*`, `TestSCM_*`, `TestGitea_*` |
| `gitlab` | GitLab CE, Argo CD | `TestCore_*`, `TestSCM_*`, `TestGitLab_*` |
| `github` | GitHub (branches of `pnz1990/kardinal-demo`), Argo CD | `TestCore_*`, `TestSCM_*`, `TestGitHub_*` |
| `delivery` | Forgejo, Argo CD, Argo Rollouts, Flagger | `TestRollouts_*`, `TestFlagger_*`, `TestDelivery_*` |
| `ui` | Forgejo, Argo CD, four more chart releases (static token and CORS, TokenReview, TokenReview without its RBAC, TLS), Playwright's Chromium | `TestUI_*` |
| `flux` | Forgejo, Flux, Prometheus Operator, Prometheus, Pushgateway, Grafana | `TestFlux_*`, `TestMetric_*`, `TestObs_*` |

`TestSCM_*` tests use only `Env.Git`, so they run against every git server;
a test that needs one provider is named after it and checks `Env.Git.Kind()`
first. The `github` suite takes its token from `KARDINAL_E2E_GITHUB_TOKEN_FILE`,
`DEMO_GITHUB_TOKEN` or `gh auth token` (`hack/e2e/components/github.sh`).

## Coverage

`test/e2e/coverage.tsv` lists every documented behavior with an id, and
whether a test covers it yet. A test claims rows with one sentence in its doc
comment, `Covers STEP-AUTO-01, SCM-CLOSED-01.`, and only for what it fully
asserts. `go test ./test/hack -run TestE2ECoverage` fails when the file and
the tests disagree, when a live test claims no row, and when no suite runs a
live test. Only live tests cover live rows; contract rows (Bitbucket, Azure
DevOps, which can't be self-hosted) are covered by unit tests.

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
