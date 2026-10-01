# kardinal-promoter — agent context

A Kubernetes-native promotion controller: Go 1.26+ backend and a React 19 UI embedded with
go:embed. All state is in Kubernetes CRDs; there is no external database.

Status: v0.8.1 is the latest release (it bundles the old forked Graph controller). main is
v0.9.0-dev and runs on upstream kro's `kro.run/v1alpha1` Graph (kro v0.10.0-rc.0, GraphKind
feature gate). Journey status: docs/aide/definition-of-done.md §Journey Status.

Claude Code sessions load this file as instructions. It changes only through a reviewed PR.

---

## Critical Thinking — Non-Negotiable

**Read this before reading anything else in this file.**

Agents on this project are expected to evaluate every idea critically — including ideas
from humans, from prior agent sessions, and from their own previous reasoning.

### The concrete failure this rule exists to prevent

The flat DAG compilation idea (#496) was embedded in the graph-purity tech debt doc, roadmap,
and vision as "the correct implementation" for months. No one verified it against the Graph controller's
actual execution model until a human explicitly challenged it. The evaluation took 10 minutes.
Graph nodes communicate through etcd-backed CRD fields — they cannot share an ephemeral
git working directory. The approach was immediately and obviously unworkable once examined.

The failure was not an implementation error. It was a process error: the idea was accepted
and propagated without verification.

### Required verification for any architectural claim

Before writing any design claim into a spec, issue, roadmap, or doc:

1. **Name the exact mechanism.** Not "nodes can communicate state" but "node A writes
   `status.commitSHA` to CRD `GitCloneTask`; node B reads `${gitClone.status.commitSHA}`
   in its template." If you cannot specify the exact mechanism, the claim is not ready.

2. **Read the source.** For Graph claims: read kro's Graph docs and
   `pkg/graphengine/` (compiler, executor) in kubernetes-sigs/kro. For reconciler
   claims: read the actual reconciler file. "It should work" without source verification
   is not acceptable.

3. **State what it cannot do.** Every approach has constraints. Identify them. A step
   that outputs a local filesystem path: can that path be represented as a CRD field and
   reconstructed across reconcile loops? If not, any approach requiring that path to cross
   a reconcile boundary will fail.

4. **Apply the adversarial question.** "What would break this?" If nothing comes to mind,
   the analysis is incomplete, not the proposal sound.

### When a human proposes something

Human input is context, not authority. "The human said so" is not a reason to implement
or document something. Evaluate the proposal against the system's actual constraints.
If the proposal has a flaw, state it clearly. Propose what achieves the underlying goal
instead. Respectful disagreement that is correct is better than agreeable agreement
that wastes weeks.

### When prior agent work proposed something

Prior agent work is subject to the same scrutiny as any new proposal. Finding that a
previously documented approach is wrong is the system working correctly — not failure.
When a flaw is found: close the issue, correct the docs, record the reasoning. Wrong
ideas that persist in documentation because they were once written with confidence are
a form of technical debt.

---

## Working on this repo

- One owner (pnz1990) and Claude Code sessions. Every session uses the owner's GitHub account.
- Build `go build ./...` · test `go test ./... -race -count=1 -timeout 120s` · lint `go vet ./...`
  and golangci-lint · vulns `govulncheck ./...` · UI `cd web && npm ci && npm run build && npm test`.
- Work on a branch or a worktree at ../kardinal-promoter.<branch>; one PR per change; squash merge.
- GitHub comments, issues and reviews written by a session start with a badge:
  `[🎯 COORDINATOR]` for the lead session, `[🔨 ENGINEER]` for a fix session.
- Work is tracked in GitHub issues with the labels below. There is no queue, state file, report
  issue or batch cadence.
- When something needs the owner, stop and ask in the session, or label the issue `needs-human`
  with one comment. Do not work around it.

---

## Architecture

```
User writes: Pipeline CRD + PolicyGate CRDs
CI creates:  Bundle CRD (via POST /api/v1/bundles)

kardinal-controller:
  Bundle → translator generates kro Graph (per-Bundle, tailored to intent)
  kro's Graph controller creates PromotionStep + PolicyGate CRs in DAG order
  PromotionStep reconciler: git-clone → kustomize-set-image →
                            git-commit → open-pr → wait-for-merge → health-check
  PolicyGate reconciler: evaluates CEL → status.ready + lastEvaluatedAt
  Graph advances on readyWhen satisfied
  Failure → the Graph does not advance past the Failed step; `onHealthFailure: rollback`
            or a RollbackPolicy promotes the previous Bundle (docs/rollback.md)

All state in etcd. kubectl is sufficient.
```

A PromotionStep starts only when every gate in its `spec.requiredGates` exists, is ready, and
was evaluated at or after the step's creationTimestamp (`checkRequiredGates`,
pkg/reconciler/promotionstep); the PolicyGate reconciler re-evaluates a new step's gates at once.
PolicyGate `spec.when` is deprecated and ignored: pre-deploy and post-deploy behave the same, and
`kardinal validate` warns when it is set. Do not set `when:` in examples or docs.

## Package Layout

```
cmd/
  kardinal/                 # CLI
  kardinal-controller/      # controller binary
pkg/
  cel/                      # CEL library adapted from kro (library/, conversion/); PolicyGate only
  graph/                    # Graph builder + client (Pipeline + Bundle → kro Graph)
  health/                   # health Watch nodes (resource, Argo CD, Flux, Argo Rollouts, Flagger)
  lifecycle/                # pause/resume, rollback, promote (shared by CLI, UI API, reconcilers)
  reconciler/
    bundle/  changewindow/  eventfilter/  metriccheck/  notificationhook/  observability/
    pipeline/  policygate/  promotionstep/  prstatus/  rollbackpolicy/  scheduleclock/
    subscription/
  scm/                      # GitHub, GitLab, Bitbucket, Azure DevOps, Forgejo providers
  source/                   # Subscription watchers (OCI registry, Git)
  steps/                    # step engine + built-in steps
  translator/               # Bundle → Graph translation
  uiauth/                   # UI TokenReview + SubjectAccessReview
web/
  embed.go                  # go:embed all:dist
  src/                      # React 19 UI
```

## CEL — two contexts; do not mix them

1. PolicyGate `spec.expression` — evaluated by the PolicyGate reconciler
   (pkg/reconciler/policygate/cel_evaluator.go, newEvaluator) and by `kardinal policy simulate`
   (cmd/kardinal/cmd/policy_eval.go, the same package). The full list of variables and functions
   is docs/reference/cel-context.md; TestDocumentedCELContext fails when the doc and the
   environment differ. Read it before you write or document an expression.
   - Variables: bundle.* (including labels, intent.targetEnvironment, upstreamSoakMinutes, pr[...]),
     schedule.*, environment.name, metrics.*, upstream.<env>.soakMinutes, changewindow.<name>.
   - `metrics.<name>.value` (string), `.result` ("Pass", "Fail" or "Stale") and `.stale` (bool).
     A MetricCheck result past its `status.validUntil` is stale: `.result` is "Stale", `.value` is
     "" and `.stale` is true. Write `metrics["x"].result == "Pass"`, not `!= "Fail"`, which
     passes on a stale result.
   - Functions: cel-go string extensions; json.marshal / json.unmarshal; `m1.merge(m2)` (member
     form only; there is no global maps.merge); lists.setAtIndex / insertAtIndex / removeAtIndex;
     random.seededInt / random.seededString; changewindow.isAllowed / isBlocked.
   - They come from kardinal's pkg/cel/library, adapted from kro's library. kardinal imports no
     github.com/kubernetes-sigs/kro Go module; do not add one. `omit()` is not available here.
2. kro Graph CEL (node templates, readyWhen, includeWhen, forEach) — evaluated by kro (kro
   pkg/cel/environment.go). Variables are the node IDs in scope. kro adds hash and omit; omit is
   rejected in readyWhen, includeWhen and forEach. schedule.*, metrics.* and changewindow.* do
   not exist here.

ScheduleClock is not a Graph node. The PolicyGate reconciler watches ScheduleClock objects and
re-evaluates gate instances on every status.tick; Graph nodes see only the gate's status.ready.

Valid PolicyGate examples:

```
!schedule.isWeekend && schedule.hour >= 9 && schedule.hour < 17
bundle.provenance.author != "dependabot[bot]"
upstream.uat.soakMinutes >= 30
metrics["error-rate"].result == "Pass"
!("release-type" in bundle.labels) || bundle.labels["release-type"] != "hotfix"
```

## E2E Testing Infrastructure

The live e2e tests are in test/e2e/live, behind the `e2e` build tag. Each suite (hack/e2e/up.sh
defines them) gets its own kind cluster with a real git server, a GitOps engine and the
controller built from the checkout. test/e2e/README.md has the details.

```bash
make e2e-up SUITE=core          # hack/e2e/up.sh core: kind cluster kardinal-e2e-core + components
make test-e2e-live SUITE=core   # hack/e2e/run.sh core: the suite's tests
make e2e-down SUITE=core        # delete the kind cluster
```

- **A skipped live test fails CI.** run.sh fails when a test fails or skips, or when no test
  ran. A missing cluster, component or credential fails the test.
- `.github/workflows/e2e-live.yml` runs every suite on every PR, on pushes to main and weekly
  (each test three times); core runs on three Kubernetes minors. It needs no repo secret. Its
  `e2e live` job runs `go run ./test/e2e/proof` on the suites' results and fails when a
  coverage row's test failed, skipped or did not run.
- test/e2e/coverage.tsv has one row per documented behavior. A live test claims rows with one
  sentence in its doc comment, `Covers STEP-AUTO-01, SCM-CLOSED-01.`, only for what it fully
  asserts. `go test ./test/hack -run TestE2ECoverage` fails when a row's status and the tests
  disagree, when a live test claims no row, or when no suite runs it.

**Multi-cluster (J2):** there is no live multi-cluster setup in this repo. J2 evidence is
tracked in #1293.

**Test application**: podinfo (`ghcr.io/stefanprodan/podinfo`) at the real tags pinned in
test/e2e/fixtures (`fixtures.V1`..`V3`); `fixtures.BrokenTag` gives a rollout that never becomes
Available. Live tests use these fixtures, not kardinal-test-app or a placeholder image.

## Go Standards

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```
- `fmt.Errorf("context: %w", err)` — no bare errors
- zerolog via `zerolog.Ctx(ctx)` — no fmt.Println
- Table-driven tests with `testify/assert` + `require`
- `go test -race` always
- Conventional Commits: `feat(scope): desc`, `fix(scope): desc`
- No `util.go`, `helpers.go`, `common.go` (CI enforces)
- Every reconciler: idempotent, safe to re-run after crash

## Banned Filenames (CI enforces)

`util.go`, `helpers.go`, `common.go`

## Label Taxonomy

All issues must have labels from each of these groups:

| Group | Labels | Applied to |
|---|---|---|
| Kind | `kind/enhancement`, `kind/bug`, `kind/chore`, `kind/docs`, `kind/security` | All issues |
| Area | `area/controller`, `area/graph`, `area/policygate`, `area/cli`, `area/ui`, `area/scm`, `area/health`, `area/api`, `area/test`, `area/docs` | All issues |
| Priority | `priority/critical`, `priority/high`, `priority/medium`, `priority/low` | All issues |
| Size | `size/xs`, `size/s`, `size/m`, `size/l`, `size/xl` | Item issues |
| Type | `epic` | Epic issues only |
| Workflow | `needs-human`, `blocked`, `blocked-on-upstream` | Set by sessions |

## Anti-Patterns (review blocks PRs containing these)

| Pattern | Caught by |
|---|---|
| An issue or checklist item marked done without an implementation | review |
| Mutating Deployments/Services directly | review |
| **Any `github.com/kubernetes-sigs/kro` Go module in go.mod** | review |
| Missing Apache 2.0 header | review |
| Banned filenames | CI |
| No idempotency test on reconciler | review |
| Feature not in user docs | review |
| go.mod not tidy | CI |
| **Business logic evaluated outside a Graph node or reconciler that writes to CRD status** | **review — Graph-first violation → stop and ask the owner** |
| **New usage of `pkg/cel` outside `pkg/reconciler/policygate`** | **review — Graph-first violation → stop and ask the owner** |
| **Reconciler that makes decisions based on fields not written to its own CRD status** | **review — Graph-first violation → stop and ask the owner** |
| **CEL FunctionBinding that makes HTTP calls or external I/O** | **review — Graph-first violation → stop and ask the owner** |
| **Dependency between components expressed as in-memory state, not CRD fields** | **review — Graph-first violation → stop and ask the owner** |
| **Bypassing Graph for "simple" promotion cases** | **review — Graph-first violation → stop and ask the owner** |
| **`time.Now()` or `time.Since()` called outside a CRD status write** | **review — Graph-first violation → stop and ask the owner** |
| **External HTTP call (GitHub API, Prometheus, webhook) in reconciler hot path** | **review — Graph-first violation → stop and ask the owner** |
| **Cross-CRD status mutation (reconciler for CRD A writing to CRD B's status)** | **review — Graph-first violation → stop and ask the owner** |
| **`exec.Command()` or subprocess in reconciler** | **review — Graph-first violation → stop and ask the owner** |
| **In-memory struct passing state between reconcile iterations** | **review — Graph-first violation → stop and ask the owner** |

**Complete logic leak catalog with GitHub issues**: `docs/design/11-graph-purity-tech-debt.md`
This document lists every known place where business logic leaks outside the Graph layer,
categorized by severity and elimination path. Every new feature must not introduce new leaks.

### Graph-first

The Graph is the only place promotion order and gating are decided. A node becomes ready
through `readyWhen`; a node is created only when `includeWhen` holds and every expression it
references resolves (resolvability gating: a node that references a field not yet written
waits). There is no `propagateWhen`. Reconcilers write their decisions to their own CRD
status, and the Graph reads that status.

**Before implementing ANY new feature, answer these questions in order:**

1. Can this be a **Watch node**? (Read an existing K8s resource into Graph scope — no reconciler needed)
2. Can this be an **Owned node** whose reconciler writes `status.ready`? (Graph watches the status)
3. Can this be a **CEL library extension** on the Graph environment? (Stateless, cheap, synchronous only)

If none apply: **STOP and ask the owner** (in the session, or `needs-human` on the issue) with
the architectural question. Do not implement a workaround. Do not reference `pkg/cel` in new
code.

The accepted exception (ledger G8, docs/design/10) is `pkg/cel` used only by
`pkg/reconciler/policygate` and the CLI simulate path through that package; it must not grow.

---

## kro Upgrade Cadence

kardinal targets upstream [kro](https://github.com/kubernetes-sigs/kro)'s Graph kind
(`kro.run/v1alpha1`, `GraphKind` feature gate). Graph is alpha: breaking changes can land
between releases, and new primitives can eliminate existing workarounds.

kro is installed separately — the kardinal chart does not bundle it. `KRO_VERSION` in
`hack/install-kro.sh` is the single source of truth for which kro version kardinal targets.

**Agents must actively manage kro upgrades.** An upgrade is:
1. Update `KRO_VERSION` in `hack/install-kro.sh`
2. Update the `kro.version` annotation in `chart/kardinal-promoter/Chart.yaml`
3. Run compat checks (see upgrade protocol)
4. Open a PR

### When to check for new releases

At the start of any session that touches pkg/graph or pkg/translator, and before every
release, run:

```bash
PINNED=$(grep 'KRO_VERSION:-' hack/install-kro.sh | grep -o '[0-9][0-9a-z.-]*' | head -1)
LATEST=$(gh api 'repos/kubernetes-sigs/kro/releases?per_page=1' --jq '.[0].tag_name')
echo "Pinned v$PINNED, latest $LATEST"
```

**If `$LATEST` is newer than the pin**: open a `chore(graph): review and upgrade kro` issue,
or do the upgrade if it is in scope for the session. This is mandatory, not optional. See the
upgrade protocol below.

### Upgrade protocol (when you upgrade)

```bash
# 1. Clone kro and read the log since our pin
PINNED=$(grep 'KRO_VERSION:-' hack/install-kro.sh | grep -o '[0-9][0-9a-z.-]*' | head -1)
git clone -q https://github.com/kubernetes-sigs/kro.git /tmp/kro-review
cd /tmp/kro-review && git log v${PINNED}..<new-tag> --oneline -- pkg/graphengine/ api/v1alpha1/

# 2. Read diffs for the change surfaces most likely to break kardinal
git diff v${PINNED}..<new-tag> -- api/v1alpha1/
git diff v${PINNED}..<new-tag> -- pkg/graphengine/compiler/
git diff v${PINNED}..<new-tag> -- pkg/graphengine/executor/ | head -200

# 3. For each breaking change found, identify the kardinal file and line that needs updating.
#    Common breakage vectors:
#    - Node ID format requirements (compiler/validation.go)
#    - Graph condition type renames (api/v1alpha1)
#    - readyWhen / includeWhen / forEach semantic changes (compiler, executor, Graph docs)
#    - ref node shape and impersonation (spec.serviceAccountName) changes
```

After analysis, either:
- **Open a kardinal PR** with the compat fixes (update `hack/install-kro.sh`, the `Chart.yaml`
  annotation, node ID invariants, comment updates, test fixture updates)
- **Open a kro issue** if the change is a kro bug (see §Upstream issues below)
- **Both** if the change is a kro design evolution that requires coordination

### Primitive rethink (every 5th upgrade or on major kro releases)

When a kro upgrade introduces a substantial new capability (new node kinds, new `ref`
semantics, new CEL functions, new gating model), the upgrading engineer must also answer —
in the PR description or as a follow-up issue:

> **Does this new kro capability let us delete or simplify something in kardinal?**

Specifically check:
- Can any `pkg/reconciler/*` reconciler be deleted because kro now handles
  the pattern natively?
- Can `pkg/translator/translator.go` be simplified because kro now expresses
  something that required hand-built Graph specs?
- Do any open gaps in `docs/design/16-graph-capability-ledger.md` or `blocked-on-upstream`
  GitHub issues now have a solution?
  ```bash
  gh issue list --repo pnz1990/kardinal-promoter --label blocked-on-upstream --state open
  ```
- Are our resolvability-gating patterns and node ID conventions still idiomatic, or does the
  new kro suggest a cleaner approach?

If a simplification is found: open a `kind/enhancement,area/graph` issue describing
it. Do not gold-plate the upgrade PR itself — file the simplification separately.

### Upstream issues and PRs

When kardinal hits a kro bug or missing primitive, engage upstream directly. Record the gap
in `docs/design/16-graph-capability-ledger.md` first. Do not silently work around kro
limitations.

**Open a kro issue when:**
- A kro bug causes a kardinal feature to fail
- A kro API change breaks our integration in a way that seems unintentional
- kro's runtime behavior diverges from its own Graph docs

**Open a kro PR when:**
- A missing primitive forces a workaround that violates Graph-first architecture
- A validation is wrong for real-world use
- The fix is small, well-scoped, and has a test case

```bash
# Issue template
gh issue create --repo kubernetes-sigs/kro \
  --title "fix: <specific symptom in terms of kro Graph internals>" \
  --body "## What kardinal-promoter observed
<concrete behaviour, ideally with a minimal Graph spec that reproduces it>

## Root cause (from source reading)
<specific file:line and why>

## Suggested fix
<if known — a diff is ideal>"

# PR: fork, branch, fix, test, open
gh repo fork kubernetes-sigs/kro --clone
cd kro && git checkout -b fix/<name>
# ... fix ...
gh pr create --repo kubernetes-sigs/kro --title "fix: ..." --body "..."
```

After opening: cross-link the kro issue/PR in the kardinal issue that motivated it and in the
ledger entry. Label the kardinal issue `blocked-on-upstream` if we must wait for upstream.
When the upstream change lands: upgrade our pin, remove the workaround, close the
kardinal issue, and update the ledger.

---

## Branch policy

main is protected: a PR is required, the required status checks must pass, and enforce_admins is
on. It needs 0 approvals because every session uses the owner's account, and GitHub does not let
an account approve its own PR. PRs are squash-merged; merge commits and rebase merges are not
used. There are no exceptions: no direct push to main, no `gh pr merge --admin`, and no change
to branch protection or rulesets.

Merge a green PR with `gh pr merge <n> --squash --delete-branch` only when the owner has
authorized merging in this session, and only if pnz1990 or dependabot[bot] opened it. Never merge
a PR from anyone else that touches AGENTS.md, CLAUDE.md, .claude/, .github/workflows/ or
.github/actions/; leave it for the owner. The next session loads AGENTS.md as its instructions,
and workflows run with repo secrets. The `agent instructions guard` check
(.github/workflows/agent-instructions-guard.yml) fails on such PRs.

## Releases

- Tag only a commit on main, and never move or delete a published tag. release.yml does not
  check the tagged commit yet; until it does, check it yourself
  (`git merge-base --is-ancestor <sha> origin/main`).
- Prereleases are vX.Y.Z-rc.N. helm skips them unless you pass --version, so the docs pin the
  chart version.
- Before tagging: docs/changelog.md has the section with upgrade notes; README.md, docs/quickstart.md
  and docs/installation.md show the new version; the kro pin (hack/install-kro.sh, the Chart.yaml
  kro.version annotation, the release notes text in release.yml) matches kro's latest release;
  J1–J6 have e2e-live evidence on the tagged commit (§Journey validation).
- After tagging, check the pages that drift: /roadmap/, /comparison/ (maturity row), / and
  /changelog/ on https://pnz1990.github.io/kardinal-promoter/.

## Journey validation

A journey counts as passing only with live evidence: the link to an e2e-live run
(`.github/workflows/e2e-live.yml`) on the commit in which the live tests covering the journey's
steps passed. The `e2e live` job summary lists each coverage.tsv row's result. Record the run
link in docs/aide/definition-of-done.md §Journey Status. TestJourneyN (fake client) is a unit
test, not evidence.

```bash
gh run list --repo pnz1990/kardinal-promoter --workflow=e2e-live.yml --commit <sha>
gh workflow run e2e-live.yml --repo pnz1990/kardinal-promoter --ref <branch>   # -f count=N repeats each test
```

## Journey Self-Validation Commands

Read docs/aide/definition-of-done.md for the journey's steps, then run the live tests that
cover them on a suite cluster. RUN narrows the suite to some tests:

```bash
make e2e-up SUITE=core
make test-e2e-live SUITE=core RUN='^TestGate_Pause'   # only the pause tests
make e2e-down SUITE=core
```

Journey 2 (multi-cluster) has no live setup yet; evidence is tracked in #1293. The fake-client
journey tests run without a cluster: `go test ./test/e2e/ -run TestJourney` (TestJourney5CLI
needs `make build` first).

## Product invariants

- Kubernetes is the control plane: every object is a CRD and kubectl is enough; CLI, UI and
  webhooks only create and read CRDs.
- The controller never writes workload resources (Deployments, Services, routes); changes go
  through Git.
- The PR is the approval surface; its body carries the evidence.
- Promotions move versioned artifacts with provenance, not opaque diffs.
- A rollback is a forward promotion of an earlier Bundle through the same gates and audit trail.

## Product Validation Scenarios

The live suites replace the hand-run scenarios that were here (happy path, pause, weekend
gate, explain, rollback, supersession). Each documented behavior is a row in
test/e2e/coverage.tsv; e2e-live.yml runs the tests that cover them. A `todo` row has no live
test yet. Live tests use kardinal as a customer would, against real components, with nothing
mocked (test/e2e/README.md §Rules).

To add a scenario:

1. Add its row to coverage.tsv, or pick its `todo` row.
2. Write the test in test/e2e/live, with `Covers ID.` in its doc comment and a name that a
   suite's RUN pattern in hack/e2e/up.sh matches.
3. Set the row to `covered` and run `go test ./test/hack -run TestE2ECoverage`.
4. Run the suite: `make e2e-up SUITE=<suite>` and `make test-e2e-live SUITE=<suite>`.
