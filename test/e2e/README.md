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
the default is `test/e2e/kind-config.yaml`'s).

The scripts use the kind, kubectl and helm versions pinned in
`hack/tool-versions.env`, as CI does: `hack/e2e/tools.sh` downloads them into
`bin/e2e`, checks their sha256, and puts `bin/e2e` first on PATH (older kind
releases can't boot the node images, and helm versions word errors
differently). `make e2e-tools` installs them up front; the chart tests in
`test/helm` then use `bin/e2e/helm` too. The pins are linux-amd64 binaries;
on another platform, or with `KARDINAL_E2E_TOOLS=path`, the tools on PATH are
used, with a warning for each version that differs. The core suite also
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
the upgrade suite on Kubernetes 1.30, the oldest supported minor, and the
newest minor.
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
| `flux` | Forgejo, Flux, Prometheus Operator, Prometheus, Pushgateway, Grafana, fake Datadog, New Relic, CloudWatch and web metrics APIs (`hack/e2e/metricsapi`) that check credentials as the real services do | `TestFlux_*`, `TestMetric_*`, `TestObs_*` |
| `chart` | Forgejo, Argo CD, cert-manager, the webhook receiver, Jaeger; no controller release: each test installs its own | `TestChart_*`, `TestDeprecated_*` |
| `upgrade` | Forgejo, Argo CD, kardinal-promoter v0.8.1 with its bundled Graph controller and no kro; the test follows the upgrade guide, so a cluster serves one run. `KIND_K8S=1.30` runs it on Kubernetes 1.30 | `TestUpgrade_*` |
| `multi-cluster` | Forgejo, Argo CD, Flux and Argo Rollouts in the hub, and a second kind cluster (`<cluster>-spoke`, Argo Rollouts) registered with the hub's Argo CD and Flux | `TestMultiCluster_*` |
| `shard` | Forgejo, Argo CD, and two controllers splitting the namespaces: the main release as shard `default`, `components/shard.sh`'s release as shard `b` | `TestShard_*` |
| `scale` | Forgejo behind Toxiproxy, Prometheus Operator and Prometheus, two controller replicas built with `-race` | `TestScale_*` (see [Scale suite](#scale-suite)) |

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

## Scale suite

The `scale` suite tests kardinal the way a large company runs it: Pipelines
of over 100 stages and 150 environments, hundreds of Pipelines, bursts of
1,000 Bundles, operators and CI racing the controller, and faults in every
dependency. Each test ends with the invariants checker
(`test/e2e/framework/invariants`), which any live test can run.

```bash
KIND_CLUSTER=kp-scale KARDINAL_E2E_BUILD=host KARDINAL_E2E_NODE_MEMORY=24g KARDINAL_E2E_KRO_MEMORY=8Gi make e2e-up SUITE=scale
KIND_CLUSTER=kp-scale make test-e2e-live SUITE=scale                          # the ci profile
KIND_CLUSTER=kp-scale KARDINAL_E2E_SCALE_PROFILE=full KARDINAL_E2E_TIMEOUT=4h make test-e2e-live SUITE=scale
KIND_CLUSTER=kp-scale KARDINAL_E2E_SCALE_PROFILE=soak KARDINAL_E2E_TIMEOUT=3h RUN='^TestScale_LoadSustained$' make test-e2e-live SUITE=scale
```

The controller is built with `-race` (`KARDINAL_E2E_RACE=1`, the suite's
default; on the host, with cgo, whatever `KARDINAL_E2E_BUILD` says) and runs
two replicas, so a killed leader fails over. It reaches Forgejo through
Toxiproxy (`hack/e2e/components/toxiproxy.sh`), which the git chaos tests
slow down and cut off; the test runner reaches Forgejo directly. Every
environment's health check reads one Deployment that runs the pause image,
so the suite measures kardinal rather than a GitOps engine syncing 150
environments: the invariants read what kardinal wrote to git.

`KARDINAL_E2E_SCALE_PROFILE` sets the sizes (`test/e2e/framework/scale/profile.go`):

| Profile | Sizes | Time |
|---|---|---|
| `ci` (default) | 30-stage chain, canary and 3 waves x 4 regions, 20 Pipelines, a burst of 100 Bundles, 0.5 Bundles/s for 2 min, chaos for 3 min, 10 min to settle; sized for a GitHub-hosted runner (4 vCPUs, 16 GB), 4 tests at a time (`KARDINAL_E2E_PARALLEL`) | 25 min on a 32-core host |
| `full` | 100-stage chain, canary and 9 waves x 11 regions (100 environments), fan-in of 50, 200 Pipelines, a burst of 1,000 Bundles over 100 Pipelines, 2 Bundles/s for 10 min, chaos for 10 min | 2 h on a 32-core host; give the kind node 24 GB (`KARDINAL_E2E_NODE_MEMORY=24g`) and kro 8 GB (`KARDINAL_E2E_KRO_MEMORY=8Gi`) until #1492 is fixed |
| `soak` | `full`, with 5 Bundles/s for 30 min over 100 Pipelines | `full` plus 40 min |

Any size can be set on its own: `KARDINAL_E2E_SCALE_<FIELD>`, the field name
in upper snake case (`KARDINAL_E2E_SCALE_SUSTAINED_RATE=5`,
`KARDINAL_E2E_SCALE_SUSTAINED_FOR=1h`, `KARDINAL_E2E_SCALE_PIPELINES=500`).

| Tests | What they do |
|---|---|
| `TestScale_Topology*` | a 100-stage chain, the 120-stage chain and 150-environment fan-out a large company asks for, waves, a diamond lattice, a fan-in, mixed auto and pr-review approval, several Pipelines writing one repo and branch |
| `TestScale_Load*`, `TestScale_LatencySLO` | 200 Pipelines with a Bundle each, a burst of 1,000 Bundles (the newest per Pipeline must end Verified), a sustained rate for a duration, the latency objective ([Latency SLO](#latency-slo)) |
| `TestScale_Race*` | rapid-fire Bundles, Pipeline edits, gate flapping, a ChangeWindow switched on while a step waits for merge, pause/resume storms, rollback during a promotion, PRs closed, reopened and merged from outside, a force-pushed branch, a namespace deleted mid-flight, duplicate, forged and out-of-order webhooks |
| `TestScale_Chaos*` | the leader killed every 20-60 s, kro restarted, git latency and outages, API Priority and Fairness throttling the controller to one seat, the SCM token rotated mid-flight |

The load and chaos tests run one at a time, first (`scale.Begin`); the
topology and race tests then run in parallel (`scale.BeginParallel`), so
their work queue and goroutine checks are reported, not enforced: other
tests load the same controller.

The invariants, after every Bundle settled:

- every environment's git content is the image of the last Bundle Verified there;
- no environment has two open PRs, and no open PR belongs to a finished Bundle;
- no `kardinal/` branch is left without an open or merged PR;
- every Bundle (and each of its steps) reached a terminal phase within the profile's `Settle`;
- no Graph outlived its Bundle, stayed deleting, reports an error or nears etcd's request limit;
- AuditEvents agree with the step states;
- the controller logged no `DATA RACE`, no panic and no error-level line outside the allowlist (`invariants.Benign` plus the faults a test injects), and neither its containers nor kro's restarted (OOMKilled, crashed);
- Prometheus: the reconcile error ratio stays under the test's limit, every work queue drains, and no controller Pod that ran the whole test in one role (leader or standby) grew its goroutines past 1.5x (+100), its resident memory past 2x (+200 MiB; 2.5x + 500 MiB for a `-race` build, whose shadow memory grows with every allocation and is never returned: steady leaders measured up to 2.3x and +261 MiB in the `full` profile) or its memory past 90% of the limit.

Each test writes `diagnostics/scale/<test>/report.json` (every number:
latency per stage, Bundle end to end, Graph sizes, reconcile errors, queue
depth, memory and goroutines per Pod) and `report.md`, and keeps the raw
controller and kro logs next to them.

### Latency SLO

`TestScale_LatencySLO` gives each of `SLOPipelines` Pipelines, with three
automatic environments each, one Bundle at once, as a monorepo release
does. It then holds the controller to the profile's objective: the
`latency-slo` invariant (`invariants.SLO`, any test can set
`Options.SLO`). Step latency runs from a PromotionStep's creation, once its
upstream environments are Verified and its gates have passed, to its
Verified condition, for automatic environments only. Bundle latency runs
from the Bundle's creation to its last environment Verified. The
objectives allow for the `-race` build, which makes each reconcile about
5x slower:

| Profile | Pipelines | Auto step p50 | Auto step p99 | Bundle end to end p99 |
|---|---|---|---|---|
| `ci` | 20 | 5 s | 15 s | 45 s |
| `full`, `soak` | 200 | 10 s | 30 s | 2 min |

`KARDINAL_E2E_SCALE_SLO_STEP_P50`, `_SLO_STEP_P99`, `_SLO_BUNDLE_P99` and
`_SLO_PIPELINES` override them. With the reconciler worker defaults
(#1509) the `full` run measures automatic steps p50 2 s and p99 5 s, and
Bundles p99 92 s (docs/installation.md, Controller concurrency); with one worker
each it was a step p50 of 65 s.

A test that reproduces an open bug calls `scale.KnownBug(t, issue, ...)`
and runs on: it is an expected failure. When it fails, `test/e2e/report`
lists it as a known bug (`xfail`) instead of a failure. When it passes,
`KnownBug` fails it with `KNOWN BUG #<issue> FIXED`: remove the call and mark
its coverage rows `covered` (they are `known-bug` until then).
`test/e2e/proof` fails a known bug whose issue is closed; it asks the
GitHub API when `GITHUB_TOKEN` is set, as in CI.

## Coverage

`test/e2e/coverage.tsv` lists every documented behavior with an id, and
whether a test covers it yet. A test claims rows with one sentence in its doc
comment, `Covers STEP-AUTO-01, SCM-CLOSED-01.`, and only for what it fully
asserts. `go test ./test/hack -run TestE2ECoverage` fails when the file and
the tests disagree, when a live test claims no row, when no suite runs a live
test, and when a row's `suite` (a suite in `hack/e2e/up.sh`, or `unit` for a
contract row) runs none of its tests. It also fails when a `source` ref is
not a repo path, `path:N` or `path:N-M`, names lines past the end of its
file, or starts on a blank line, a bare `---` or a markdown table
separator. Only live tests cover live rows;
contract rows are covered by unit tests: Bitbucket and Azure DevOps, which
can't be self-hosted, behaviors a live test cannot force, such as a race
between two status writes, and a Kubernetes older than 1.30, which no suite
boots.

Each suite run writes `test/e2e/results/<cluster>/summary.json`. The `e2e
live` job runs `go run ./test/e2e/proof` on every suite's summary: it fails
when a claimed row's test failed, skipped or didn't run, and the job summary
lists every row's result. `-complete` also fails on rows still todo.

## Rules

- **A live test never skips.** A missing cluster, component or credential
  fails the test. CI treats a skipped test as a failure too. A scale test
  that reproduces an open bug still runs, as an expected failure
  (`scale.KnownBug`).
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
  `KARDINAL_E2E_WAVE_ENVS=150` runs `TestHealth_ArgoCDWaveOnSharedBranch` (core) with a wave of
  150 environments on one branch, the size of the #1575 acceptance case, instead of 30.
- Repos are created at most 4 at a time (`KARDINAL_E2E_GIT_CREATE_SLOTS`), and a
  Forgejo or Gitea create that times out or gets a 5xx is retried from scratch:
  when a shard's parallel tests start together, about 100 creates at once
  queued past the client's 30s timeout (#1557).

## Test app

Fixtures deploy [podinfo](https://github.com/stefanprodan/podinfo) at pinned
real tags (`fixtures.V1`..`V3`). It has a readiness probe and Prometheus
metrics, so the same app drives health, canary and MetricCheck tests.
`fixtures.BrokenTag` does not exist, which gives a rollout that never
becomes Available.
