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

## Package Layout

```
cmd/
  kardinal/                 # CLI
  kardinal-agent/           # distributed agent binary (not shipped; #1263)
  kardinal-controller/      # controller binary
pkg/
  admission/                # Pipeline validating webhook (cycles, cross-namespace secretRef)
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

The live validation loop is `.github/workflows/pdca.yml` (see §Product Validation Scenarios).

**Single-cluster setup** (kind, all environments):
```bash
make e2e-setup           # kind + kro + kardinal built from this checkout + quickstart fixtures
make test-e2e-kind       # cluster e2e tests (build tag e2e) against that kind cluster
make setup-e2e-env       # kind + kro + Argo CD + test/uat/prod
make kind-down           # delete the kind cluster
```

**Multi-cluster (J2):** there is no live multi-cluster setup in this repo. J2 evidence is
tracked in #1293.

**Test application**: `github.com/pnz1990/kardinal-test-app`
- Image: `ghcr.io/pnz1990/kardinal-test-app:sha-<7chars>`
- Get latest SHA: `gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]'`

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
  J1–J6 have live kind evidence on the tagged commit.
- After tagging, check the pages that drift: /roadmap/, /comparison/ (maturity row), / and
  /changelog/ on https://pnz1990.github.io/kardinal-promoter/.

## Journey validation

A journey counts as passing only with live-cluster evidence: a PDCA workflow run (pdca.yml;
results are in the job summary) or a `[LIVE CLUSTER VALIDATED]` comment on the PR or release
issue. The comment gives the commands, their output, the kind or EKS version and the
kardinal-test-app image SHA. Record the run or comment link in docs/aide/definition-of-done.md
§Journey Status. TestJourneyN (fake client) is a unit test, not evidence.

```bash
# Trigger the PDCA workflow for one scenario (1-6) or all of them
gh workflow run pdca.yml --repo pnz1990/kardinal-promoter -f scenario=1
gh run list --repo pnz1990/kardinal-promoter --workflow=pdca.yml --limit 3
```

PDCA's first step checks that the `KARDINAL_DEMO_PAT` secret can read pnz1990/kardinal-demo.
When it fails with "rotate it", the owner replaces the PAT; a session cannot.

## Journey Self-Validation Commands

Read docs/aide/definition-of-done.md and run the relevant journey steps on a kind cluster
(`make e2e-setup`, or by hand: `bash hack/install-kro.sh`, then install the chart):

```bash
# Journey 1 (Quickstart)
kubectl apply -f examples/quickstart/pipeline.yaml
kardinal get pipelines
kardinal explain kardinal-test-app --env prod

# Journey 2 (Multi-cluster) — no live setup yet; evidence is tracked in #1293

# Journey 3 (Policies)
kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Saturday 3pm"
# must return: RESULT: BLOCKED

# Journey 4 (Rollback)
kardinal rollback kardinal-test-app --env prod
# must open PR with kardinal/rollback label

# Journey 5 (CLI)
kardinal version
kardinal get pipelines
kardinal explain kardinal-test-app --env prod
# all must match output format in docs/cli-reference.md
```

## Product invariants

- Kubernetes is the control plane: every object is a CRD and kubectl is enough; CLI, UI and
  webhooks only create and read CRDs.
- The controller never writes workload resources (Deployments, Services, routes); changes go
  through Git.
- The PR is the approval surface; its body carries the evidence.
- Promotions move versioned artifacts with provenance, not opaque diffs.
- A rollback is a forward promotion of an earlier Bundle through the same gates and audit trail.

## Product Validation Scenarios

`.github/workflows/pdca.yml` runs these scenarios nightly and on demand on a kind cluster
(`make setup-e2e-env` gives you the same setup by hand). Use kardinal as a customer would.
Do not mock anything.

### CRITICAL: Use the real test repos, not nginx

**ALWAYS use these repos for testing — never nginx, never placeholder images:**

| Repo | Purpose | Image |
|---|---|---|
| `github.com/pnz1990/kardinal-test-app` | The application being promoted | `ghcr.io/pnz1990/kardinal-test-app:sha-<7chars>` |
| `github.com/pnz1990/kardinal-demo` | The GitOps target repo (environment branches) | Pipeline `repoURL` points here |

```bash
# Get the REAL latest image — do not use nginx or :latest
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"
echo "Using image: $TEST_IMAGE"
```

If you find yourself using `nginx` or any other placeholder image in tests, STOP and switch to
`kardinal-test-app`. The point is to validate with a real application that reflects real-world
usage.

### Setup (before running scenarios)

```bash
make setup-e2e-env

LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

# examples/quickstart/pipeline.yaml points at https://github.com/pnz1990/kardinal-demo,
# which has the environment branches (env/test, env/uat, env/prod).
kubectl apply -f examples/quickstart/pipeline.yaml
kubectl apply -f examples/quickstart/policy-gates.yaml
kubectl create secret generic github-token \
  --from-literal=token=${GITHUB_TOKEN} \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Scenario 1: Happy path promotion

```bash
kardinal create bundle kardinal-test-app --image $TEST_IMAGE
sleep 30
kardinal get pipelines
# Expected: test=Verified, uat=Verified, prod=PR open
kubectl get deployment kardinal-test-app -n kardinal-test-app-test
# Expected: READY 1/1
```

**Pass criteria**: test and uat auto-promote; prod PR opened with evidence body.

### Scenario 2: Pause blocks in-flight promotion

```bash
kardinal create bundle kardinal-test-app --image $TEST_IMAGE
kardinal pause kardinal-test-app
sleep 30
kardinal get pipelines
# Expected: PAUSED badge visible, bundle does not advance past test
kardinal resume kardinal-test-app
```

**Pass criteria**: PAUSED badge appears; promotion halts; resumes after resume.

### Scenario 3: Weekend gate blocks prod

```bash
kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Saturday 3pm" --soak-minutes 60
# Expected: RESULT: BLOCKED
kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Tuesday 10am" --soak-minutes 60
# Expected: RESULT: PASS
```

**Pass criteria**: exact BLOCKED/PASS strings returned. (Without `--soak-minutes`, the
require-uat-soak gate also blocks.)

### Scenario 4: Explain shows gate details

```bash
kardinal explain kardinal-test-app --env prod
# Expected: shows no-weekend-deploys gate with expression and current value
```

**Pass criteria**: gate name, CEL expression (`!schedule.isWeekend`), and result visible.

### Scenario 5: Rollback opens a PR

```bash
# Promote first
kardinal create bundle kardinal-test-app --image $TEST_IMAGE
sleep 60  # wait for test+uat
kardinal rollback kardinal-test-app --env prod
# Expected: PR opened with kardinal/rollback label and evidence body
```

**Pass criteria**: PR has `kardinal/rollback` label; PR body contains promotion evidence.

### Scenario 6: Concurrent bundles — correct supersession

```bash
IMAGE_A="ghcr.io/pnz1990/kardinal-test-app:sha-aaa1111"
IMAGE_B="ghcr.io/pnz1990/kardinal-test-app:sha-bbb2222"
kardinal create bundle kardinal-test-app --image $IMAGE_A
sleep 5
kardinal create bundle kardinal-test-app --image $IMAGE_B
sleep 30
kardinal get pipelines
# Expected: only IMAGE_B bundle is Promoting; IMAGE_A bundle is Superseded
```

**Pass criteria**: older bundle superseded, newer one continues.

### After running scenarios

For each scenario: record PASS/FAIL + actual output.
Open `kind/bug` issue if any scenario fails.
Open `kind/docs` issue if output doesn't match `docs/cli-reference.md`.
Record the result in docs/aide/definition-of-done.md §Journey Status with the run URL.
Tear down: `make kind-down` (or keep running for continuous validation).
