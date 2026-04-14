# kardinal-promoter — Project Agent Context

## What This Is

A Kubernetes-native promotion controller. Go 1.23+ backend + React 19 frontend,
embedded via `go:embed`. All state in Kubernetes CRDs. No external database.

**Status**: Pre-release. Design and specs complete. Implementation not started.

---

## UI/CLI Inspiration Source — kro-ui

When working on `web/src/` or `cmd/kardinal/`, read the kro-ui project at
`../kro-ui/` for UI patterns and inspiration. kro-ui is a production React
dashboard for kro (our underlying DAG engine) and contains directly applicable
patterns for kardinal's embedded UI:

**Highly applicable kro-ui features to adapt for kardinal:**
- **DAG visualization** (`web/src/components/`) — interactive node graph with
  per-node health states (alive/reconciling/degraded/error/pending). Adapt for
  the promotion DAG (each node = one environment promotion step).
- **6-state health chips** — Ready/Degraded/Reconciling/Pending/Error/Unknown
  with color coding. Adapt for Bundle.status and PromotionStep.status display.
- **CEL expression display** — YAML tab shows CEL expressions highlighted.
  Adapt for `kardinal explain` and the PolicyGate detail panel.
- **Live polling with "refreshed X ago"** — 5s polling with staleness indicator.
  Already partially in kardinal's `usePolling.ts` — compare against kro-ui's
  implementation for improvements.
- **Instance spec diff** — compare two instances field-by-field. Adapt for
  comparing two Bundle versions or two PromotionStep states.
- **Error aggregation** — cross-instance error grouping with affected count.
  Adapt for surfacing recurring promotion failures across environments.
- **Compile-error banner** — count of errors with one-click filter. Adapt for
  showing PolicyGates that are blocking prod with a one-click "show blocked" filter.

**Do not copy kro-ui code verbatim** — adapt the patterns. kardinal's domain
is promotions/bundles/gates, not RGDs/instances/RBAC. Read kro-ui for ideas,
implement for kardinal's concepts.

---

## SDLC Process

The team process lives in `.specify/memory/sdlc.md` — read it.
This file contains only project-specific context that specializes the generic process.

---

## Agent Identities

All sessions share GitHub account pnz1990. Every GitHub comment, issue, and PR
review MUST start with the agent's badge.

| Session | Role | Badge | AGENT_ID |
|---|---|---|---|
| 1 | Coordinator | `[🎯 COORDINATOR]` | `COORDINATOR` |
| 2 | Engineer 1 | `[🔨 ENGINEER-1]` | `ENGINEER-1` |
| 3 | Engineer 2 | `[🔨 ENGINEER-2]` | `ENGINEER-2` |
| 4 | Engineer 3 | `[🔨 ENGINEER-3]` | `ENGINEER-3` |
| 5 | QA | `[🔍 QA]` | `QA` |
| 6 | Scrum Master | `[🔄 SCRUM-MASTER]` | `SCRUM-MASTER` |
| 7 | Product Manager | `[📋 PM]` | `PM` |

```bash
export AGENT_ID="COORDINATOR"  # change per session
```

---

## Project Config (fills the generic SDLC placeholders)

```yaml
PROJECT_NAME:   kardinal-promoter
CLI_BINARY:     kardinal
PR_LABEL:       kardinal
REPORT_ISSUE:   1
REPORT_URL:     https://github.com/pnz1990/kardinal-promoter/issues/1
BOARD_URL:      https://github.com/users/pnz1990/projects/1
BUILD_COMMAND:  go build ./...
TEST_COMMAND:   go test ./... -race -count=1 -timeout 120s
LINT_COMMAND:   go vet ./...
VULN_COMMAND:   govulncheck ./...
```

---

## Scrum Master — Project Context

The SM knows the minimum about kardinal-promoter needed to review the SDLC:
- It is a Go project. Build: `go build ./...`. Test: `go test ./... -race`. Lint: `go vet ./...`.
- Engineers use git worktrees at `../kardinal-promoter.<branch>`.
- The coordinator spawns the SM after every `[BATCH COMPLETE]` report on Issue #1.
- Competitor for process health reference: how do Kargo and GitOps Promoter teams operate?
  (they both have active GitHub communities — check issue/PR velocity as a benchmark)

SM must NOT know about: CRD design, kro Graph, PolicyGates, promotion algorithms, CEL.

## Product Manager — Project Context

The PM knows the full product:
- kardinal-promoter is a Kubernetes promotion controller competing with Kargo and GitOps Promoter.
- Primary differentiators: DAG pipelines, visible policy gates, PR evidence, GitOps-agnostic.
- The definition-of-done has 5 journeys. J1 (Quickstart) and J3 (Policies) are the most critical for initial adoption.
- Key user docs to keep fresh: `docs/quickstart.md`, `docs/concepts.md`, `docs/policy-gates.md`.
- Competitors to monitor:
  - Kargo: https://github.com/akuity/kargo/releases (monthly releases)
  - GitOps Promoter: https://github.com/argoproj-labs/gitops-promoter/releases (weekly releases)
  - Argo Rollouts: https://github.com/argoproj/argo-rollouts/releases
  - Flux: https://github.com/fluxcd/flux2/releases
- Community to monitor for feature requests and pain points:
  - https://github.com/akuity/kargo/issues (what Kargo users are asking for that we don't have)
  - https://github.com/argoproj-labs/gitops-promoter/issues

**Public website audit — every batch:**

The public docs site is https://pnz1990.github.io/kardinal-promoter/. Every batch the PM
must audit it against the actual codebase. This is mandatory, not optional.

The four pages most likely to drift:
1. `/roadmap/` — "Currently Available" version number and feature list; "Near-Term" items
   must not describe features that are already shipped; "Planned" must not describe features
   from milestones with 0 open issues.
2. `/comparison/` — maturity row version number (must match latest git tag); feature matrix
   rows (a shipped feature showing ❌ for kardinal is a competitive mis-statement).
3. `/` (home page) — feature table rows; any inline version numbers.
4. `/changelog/` — must have an entry for every git tag. Missing entries confuse adopters.

Ground truth, checked in this order:
```bash
# 1. What is the latest released version?
git tag --list "v*" | sort -V | tail -1

# 2. What milestones are done (0 open issues)?
gh api repos/pnz1990/kardinal-promoter/milestones \
  --jq '.[] | "\(.title): open=\(.open_issues) closed=\(.closed_issues)"'

# 3. What features merged since the last tag?
git log $(git tag --list "v*" | sort -V | tail -1)..HEAD --oneline \
  | grep -E "feat|fix" | grep -v "state:\|chore(coord)"

# 4. Is a claimed feature actually implemented (not a stub)?
grep -rn "<feature-keyword>" pkg/ cmd/ | grep -v "_test\|//"
```

Common failure patterns to look for:
- Version still says v0.X when v0.X+1 is tagged
- Feature listed as "Planned (v0.X)" when that milestone has 0 open issues
- Feature listed as "Near-Term" when the implementation is already in pkg/
- Feature described as "coming soon" but code + tests exist
- Changelog has no entry for a released tag
- A "stub" warning on a feature that was actually completed (check the `Watch()` return value)

When you find a discrepancy: fix and push immediately. Do not accumulate fixes.
Docs CI deploys automatically on push to main (workflow: docs.yml, ~2 min).
Verify the deploy: `gh run list --repo pnz1990/kardinal-promoter --workflow=docs.yml --limit 1`

PM must NOT know about: SDLC process, team.yml, sdlc.md, templates, otherness-config.

---

## Architecture

```
User writes: Pipeline CRD + PolicyGate CRDs
CI creates:  Bundle CRD (via POST /api/v1/bundles)

kardinal-controller:
  Bundle → translator generates kro Graph (per-Bundle, tailored to intent)
  Graph controller creates PromotionStep + PolicyGate CRs in DAG order
  PromotionStep reconciler: git-clone → kustomize-set-image →
                            git-commit → open-pr → wait-for-merge → health-check
  PolicyGate reconciler: evaluates CEL → status.ready + lastEvaluatedAt
  Graph advances on readyWhen satisfied
  Failure → Graph stops downstream → rollback PR opened

All state in etcd. kubectl is sufficient.
```

## Package Layout

```
cmd/kardinal-controller/    # controller binary
cmd/kardinal/               # CLI binary
pkg/
  graph/                    # Graph CRD client + builder (spec 001)
  translator/               # Pipeline → Graph translation (spec 002)
  reconciler/
    promotionstep/          # state machine + evidence (spec 003)
    policygate/             # CEL evaluation + timer recheck (spec 004)
  health/                   # Deployment/ArgoCD/Flux adapters (spec 005)
  steps/                    # Step engine + 10 built-ins (spec 008)
  scm/                      # GitHub SCM provider
  update/                   # kustomize/helm update strategies
  cel/                      # shared CEL environment
web/
  embed.go                  # go:embed all:dist
  src/                      # React 19 UI (spec 006)
```

## CEL Expressions — kro Library (READ BEFORE WRITING ANY CEL)

kardinal uses `github.com/kubernetes-sigs/kro/pkg/cel/library` for all CEL evaluation.
This gives PolicyGate expressions the **same extended function set** as kro's own
`readyWhen`/`propagateWhen` expressions. Expressions written for kro graphs work
identically in kardinal PolicyGates.

**Available CEL functions (beyond standard CEL):**

```
# JSON
json.marshal(value)                    → string   (any value to JSON string)
json.unmarshal(jsonString)             → dyn      (JSON string to value)

# Maps
maps.merge(map1, map2)                 → map      (m2 keys overwrite m1)

# Lists (all pure — return new list)
lists.setAtIndex(list, index, value)   → list
lists.insertAtIndex(list, index, value)→ list
lists.removeAtIndex(list, index)       → list

# Random (deterministic from seed — use for consistent soak calculations)
random.seededInt(min, max, seed)       → int

# Standard string extensions (via cel-go/ext)
string.format(args)                    → string
string.lowerAscii()                    → string
```

**If any part of the system evaluates CEL expressions** (backend policy gate,
CLI `policy simulate`, UI expression preview/validation) — it MUST use
`pkg/cel/NewCELEnvironment()` which registers all kro libraries. Never construct
a raw `cel.NewEnv()` without going through this package.

**Example PolicyGate expressions using extended functions:**

```
# Standard
!schedule.isWeekend()
bundle.metadata.annotations['team'] == 'platform'

# Using json functions
json.unmarshal(bundle.spec.metadata).releaseType == 'hotfix'

# Using maps/lists
maps.merge(environment.labels, bundle.labels)['env'] != 'prod'

# Multi-condition with upstream soak
!schedule.isWeekend() && upstream.uat.soakMinutes >= 30
```

## E2E Testing Infrastructure

See `docs/aide/vision.md §PDCA Architecture` for the full validation loop.

**Single-cluster setup** (kind, all environments):
```bash
make setup-e2e-env       # kind + krocodile + ArgoCD + test/uat/prod
```

**Multi-cluster setup** (kind pre-prod + EKS prod):
```bash
make eks-up                    # create EKS cluster kardinal-e2e-prod in us-east-2 (Terraform)
make setup-multi-cluster-env   # kind (test+uat) + EKS kardinal-e2e-prod (prod)
```

**Test application**: `github.com/pnz1990/kardinal-test-app`
- Image: `ghcr.io/pnz1990/kardinal-test-app:sha-<7chars>`
- Get latest SHA: `gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]'`

## Go Standards (project-specific, referenced by QA checklist in team.yml)

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

## Banned Filenames (CI + QA enforce)

`util.go`, `helpers.go`, `common.go`

## Label Taxonomy

All issues must have labels from each of these groups (read by otherness agents from this file):

| Group | Labels | Applied to |
|---|---|---|
| Kind | `kind/enhancement`, `kind/bug`, `kind/chore`, `kind/docs`, `kind/security` | All issues |
| Area | `area/controller`, `area/graph`, `area/policygate`, `area/cli`, `area/ui`, `area/scm`, `area/health`, `area/api`, `area/test`, `area/docs` | All issues |
| Priority | `priority/critical`, `priority/high`, `priority/medium`, `priority/low` | All issues |
| Size | `size/xs`, `size/s`, `size/m`, `size/l`, `size/xl` | Item issues |
| Type | `epic` | Epic issues only |
| Workflow | `kardinal` (PR_LABEL), `needs-human`, `blocked` | Set by agents |

## Anti-Patterns (QA blocks PRs containing these)

| Pattern | Caught by |
|---|---|
| Task `[x]` without implementation | QA adversarial review |
| Mutating Deployments/Services directly | QA |
| **kro controller packages in go.mod** — importing `kro/pkg/reconciler`, `kro/cmd`, `kro/api`, etc. | CI + QA |
| **`github.com/kubernetes-sigs/kro/pkg/cel/library` is ALLOWED and encouraged** — use it for all CEL evaluation | — |
| Missing Apache 2.0 header | CI + QA |
| Banned filenames | CI + QA |
| No idempotency test on reconciler | QA |
| Feature not in user docs | QA |
| go.mod not tidy | CI |
| **Business logic evaluated outside a Graph node or reconciler that writes to CRD status** | **QA — Graph-first violation → NEEDS HUMAN** |
| **New usage of `pkg/cel` outside `pkg/reconciler/policygate`** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Reconciler that makes decisions based on fields not written to its own CRD status** | **QA — Graph-first violation → NEEDS HUMAN** |
| **CEL FunctionBinding that makes HTTP calls or external I/O** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Dependency between components expressed as in-memory state, not CRD fields** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Bypassing Graph for "simple" promotion cases** | **QA — Graph-first violation → NEEDS HUMAN** |
| **`time.Now()` or `time.Since()` called outside a CRD status write** | **QA — Graph-first violation → NEEDS HUMAN** |
| **External HTTP call (GitHub API, Prometheus, webhook) in reconciler hot path** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Cross-CRD status mutation (reconciler for CRD A writing to CRD B's status)** | **QA — Graph-first violation → NEEDS HUMAN** |
| **`exec.Command()` or subprocess in reconciler** | **QA — Graph-first violation → NEEDS HUMAN** |
| **In-memory struct passing state between reconcile iterations** | **QA — Graph-first violation → NEEDS HUMAN** |

**Complete logic leak catalog with GitHub issues**: `docs/design/11-graph-purity-tech-debt.md`
This document lists every known place where business logic leaks outside the Graph layer,
categorized by severity and elimination path. Every new feature must not introduce new leaks.

**Before implementing ANY new feature, answer these questions in order:**

1. Can this be a **Watch node**? (Read an existing K8s resource into Graph scope — no reconciler needed)
2. Can this be an **Owned node** whose reconciler writes `status.ready`? (Graph watches the status)
3. Can this be a **CEL library extension** on the Graph environment? (Stateless, cheap, synchronous only)

If none apply: **STOP. Post `[NEEDS HUMAN]` with the architectural question.**
Do not implement a workaround. Do not reference `pkg/cel` in new code.

The only permitted exception is `pkg/cel/` in `pkg/reconciler/policygate` — documented as a
transitional workaround in `docs/design/10-graph-first-architecture.md`. It must not grow.

---

## krocodile Upgrade Cadence

krocodile evolves autonomously and continuously. Breaking changes arrive without
deprecation windows. New primitives eliminate existing workarounds. The pinned
commit in `hack/install-krocodile.sh` is the single source of truth for which
krocodile version kardinal targets. **Agents must actively manage this.**

### Every batch: check for new commits

Before generating any work queue, the coordinator runs:

```bash
PINNED=$(grep 'KROCODILE_COMMIT:-' hack/install-krocodile.sh | grep -o '[a-f0-9]\{7,40\}')
AHEAD=$(cd /tmp/kro-review 2>/dev/null && git fetch -q origin krocodile && \
  git log ${PINNED}..origin/krocodile --oneline 2>/dev/null | wc -l || echo "?")

echo "We are $AHEAD commits behind HEAD krocodile"
```

**If $AHEAD ≥ 5**: add a `chore(graph): review and upgrade krocodile` item to the
queue. This is mandatory, not optional. See the upgrade protocol below.

**If $AHEAD = 0**: no action needed.

### Upgrade protocol (when assigned)

```bash
# 1. Clone at HEAD and read the log since our pin
PINNED=$(grep 'KROCODILE_COMMIT:-' hack/install-krocodile.sh | grep -o '[a-f0-9]\{7,40\}')
git clone -q --depth=200 https://github.com/ellistarn/kro.git /tmp/kro-review -b krocodile
cd /tmp/kro-review && git checkout -q $(git rev-parse origin/krocodile)

NEW_COMMITS=$(git log ${PINNED}..HEAD --oneline)
echo "$NEW_COMMITS"

# 2. Read diffs for the three change surfaces most likely to break kardinal
git diff ${PINNED}..HEAD -- experimental/controller/types.go
git diff ${PINNED}..HEAD -- experimental/controller/labels.go
git diff ${PINNED}..HEAD -- experimental/controller/dag.go
git diff ${PINNED}..HEAD -- experimental/controller/controller.go | head -200

# 3. For each breaking change found, identify the kardinal file and line that needs updating.
#    Common breakage vectors:
#    - Node ID format requirements (labels.go, types.go)
#    - Graph condition type renames (types.go)
#    - Label key scheme changes (labels.go, apply.go)
#    - readyWhen/propagateWhen semantic changes (controller.go, design docs)
#    - New node reference types replacing old shape names (types.go, node.go)
```

After analysis, either:
- **Open a kardinal PR** with the compat fixes (update celSafeSlug, label guards,
  node ID invariants, comment updates, test fixture updates, pin bump)
- **Open a krocodile issue** if the change is a krocodile bug (see §Upstream Issues below)
- **Both** if the change is a krocodile design evolution that requires coordination

### Primitive rethink (every 5th upgrade or on major krocodile releases)

When a krocodile upgrade introduces a substantial new capability (Definition nodes,
new Watch semantics, new CEL functions, new propagation model), the upgrading engineer
must also answer — in the PR description or as a follow-up issue:

> **Does this new krocodile capability let us delete or simplify something in kardinal?**

Specifically check:
- Can any `pkg/reconciler/*` reconciler be deleted because krocodile now handles
  the pattern natively?
- Can `pkg/translator/translator.go` be simplified because krocodile now expresses
  something that required hand-built Graph specs?
- Do any `blocked-on-krocodile` GitHub issues now have a solution?
  ```bash
  gh issue list --repo pnz1990/kardinal-promoter --label blocked-on-krocodile --state open
  ```
- Are our `celSafeSlug`, `propagateWhen` patterns, and node ID conventions still
  idiomatic, or does the new krocodile suggest a cleaner approach?

If a simplification is found: open a `kind/enhancement,area/graph` issue describing
it. Do not gold-plate the upgrade PR itself — file the simplification separately.

### Upstream issues and PRs

When kardinal hits a krocodile bug or missing primitive, engage upstream directly.
krocodile is autonomous development — new features and fixes land fast when motivated
by real usage. Do not silently work around krocodile limitations.

**Open a krocodile issue when:**
- A krocodile bug causes a kardinal feature to fail
- A krocodile API change breaks our integration in a way that seems unintentional
- krocodile's runtime behavior diverges from its own design docs in `experimental/docs/design/`

**Open a krocodile PR when:**
- A missing primitive forces a workaround that violates Graph-first architecture
- A validation is wrong for real-world use (we've already done this: PR #109)
- The fix is small, well-scoped, and has a test case

```bash
# Issue template
gh issue create --repo ellistarn/kro \
  --title "fix: <specific symptom in terms of krocodile internals>" \
  --body "## What kardinal-promoter observed
<concrete behaviour, ideally with a minimal Graph spec that reproduces it>

## Root cause (from source reading)
<specific file:line and why>

## Suggested fix
<if known — a diff is ideal>"

# PR: fork, branch, fix, test, open
gh repo fork ellistarn/kro --clone
cd kro && git checkout -b fix/<name>
# ... fix ...
gh pr create --repo ellistarn/kro --title "fix: ..." --body "..."
```

After opening: cross-link the krocodile issue/PR in the kardinal issue that motivated
it. Label the kardinal issue `blocked-on-krocodile` if we must wait for upstream.
When the upstream change lands: upgrade our pin, remove the workaround, close the
kardinal issue, update the history table in `docs/aide/vision.md §Upstream Issue
and PR Protocol`.

## Branch Policy — What May Go Directly to main

Branch protection enforces `enforce_admins: true`. **No push bypasses the PR requirement,
including the agent account.** The only exception is the state branch `_state`.

| Allowed direct to `main` | Must go through PR |
|---|---|
| `state: …` commits to `_state` branch | All code changes |
| — | All docs changes |
| — | All workflow changes |
| — | All coordinator queue/state updates |
| — | Everything else |

The agent process generates many housekeeping commits (queue generation, docs audits,
state updates). These are not exempt. They must be squash-merged via PR like everything
else. There is no "trivial docs fix" exception.

**Consequence**: if you find yourself tempted to push directly to main, that is a sign
the task is too small to warrant tracking and should be batched into the next PR that
touches the same area.

---

## Journey Validation Standard

**A journey is NOT done until there is live-cluster evidence.** Fake-client tests
(`fake.NewClientBuilder()`) are unit tests, not E2E validation. They prove reconciler
logic; they do not prove the full stack works on a real cluster with real images.

### What counts as evidence

A journey is marked ✅ in `docs/aide/definition-of-done.md` only when **one** of:

1. The `PDCA Validation` GitHub Actions workflow posts `[PDCA AUTOMATED]` to Issue #1
   with PASS for that scenario (uses a live kind cluster + real `kardinal-test-app` image).
2. A human or agent posts `[LIVE CLUSTER VALIDATED]` to Issue #1 with:
   - The exact commands run
   - The exact terminal output
   - The kind/EKS cluster version
   - The `kardinal-test-app` image SHA used

### What does NOT count as evidence

- `TestJourneyN` passing in CI (fake client — proves logic, not cluster integration)
- A comment like "all tests pass" without output
- Marking the checkbox based on PR merge alone

### Triggering live validation

```bash
# Trigger the PDCA workflow manually for a specific scenario
gh workflow run pdca.yml --repo pnz1990/kardinal-promoter \
  -f scenario=1   # or 2, 3, 4, 5, 6, or all

# Check the result
gh run list --repo pnz1990/kardinal-promoter --workflow=pdca.yml --limit 3
```

---

## Journey Self-Validation Commands (Engineer step 3)

Engineer reads definition-of-done.md and runs the relevant journey steps:

```bash
# Journey 1 (Quickstart)
kubectl apply -f examples/quickstart/pipeline.yaml
kardinal get pipelines
kardinal explain nginx-demo --env prod

# Journey 2 (Multi-cluster)
kubectl apply -f examples/multi-cluster-fleet/pipeline.yaml
kardinal get pipelines  # must show prod-eu and prod-us

# Journey 3 (Policies)
kardinal policy simulate --pipeline nginx-demo --env prod --time "Saturday 3pm"
# must return: RESULT: BLOCKED

# Journey 4 (Rollback)
kardinal rollback nginx-demo --env prod
# must open PR with kardinal/rollback label

# Journey 5 (CLI)
kardinal version
kardinal get pipelines
kardinal explain nginx-demo --env prod
# all must match output format in docs/cli-reference.md
```

## Reporting (project-specific values for generic team.yml)

```bash
# Post to report issue
gh issue comment 1 --body "[BADGE] ## [TYPE] ..."
```

## SPECIFY_FEATURE

When running outside a git branch:
```bash
export SPECIFY_FEATURE=001-graph-integration
```

## Files Agents Must Not Modify

- `docs/aide/vision.md`
- `docs/aide/roadmap.md`
- `AGENTS.md`
- `.specify/memory/constitution.md`
- `.specify/memory/sdlc.md`
- `docs/aide/team.yml`

## Product Validation Scenarios

The standalone agent runs these scenarios during product validation (every `product_validation_cycles`
cycles). This requires a running kind cluster — use `make setup-e2e-env` to create one.
The agent uses kardinal as a customer would. It does NOT mock anything.

For multi-cluster scenarios (J2): use `make eks-up` to create the `kardinal-e2e-prod` EKS
cluster in us-east-2 (Terraform, `terraform/eks-e2e/`), then `make setup-multi-cluster-env`
which sets up kind for pre-prod and the EKS cluster for prod.

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

# Pipeline must reference kardinal-demo as the git repo
# The pipeline.yaml repoURL should point to https://github.com/pnz1990/kardinal-demo
```

If you find yourself using `nginx` or any other placeholder image in tests, STOP and switch to `kardinal-test-app`. The point is to validate with a real application that reflects real-world usage.

### Setup (before running scenarios)

```bash
# Ensure E2E environment is running
make setup-e2e-env

# Get a real image SHA from the test app CI
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

# Apply Pipeline and PolicyGates
# IMPORTANT: pipeline.yaml repoURL must point to https://github.com/pnz1990/kardinal-demo
# kardinal-demo has the environment branches (env/test, env/uat, env/prod)
kubectl apply -f examples/quickstart/pipeline.yaml
kubectl apply -f examples/quickstart/policy-gates.yaml
kubectl create secret generic github-token \
  --from-literal=token=${GITHUB_TOKEN} \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Scenario 1: Happy path promotion

```bash
kardinal create bundle test-app --image $TEST_IMAGE
sleep 30
kardinal get pipelines
# Expected: test=Verified, uat=Verified, prod=PR open
kubectl get deployment kardinal-test-app -n kardinal-test-app-test
# Expected: READY 1/1
```

**Pass criteria**: test and uat auto-promote; prod PR opened with evidence body.

### Scenario 2: Pause blocks in-flight promotion

```bash
kardinal create bundle test-app --image $TEST_IMAGE
kardinal pause test-app
sleep 30
kardinal get pipelines
# Expected: PAUSED badge visible, bundle does not advance past test
kardinal resume test-app
```

**Pass criteria**: PAUSED badge appears; promotion halts; resumes after resume.

### Scenario 3: Weekend gate blocks prod

```bash
kardinal policy simulate --pipeline test-app --env prod --time "Saturday 3pm"
# Expected: RESULT: BLOCKED
kardinal policy simulate --pipeline test-app --env prod --time "Tuesday 10am"
# Expected: RESULT: ALLOWED
```

**Pass criteria**: exact BLOCKED/ALLOWED strings returned.

### Scenario 4: Explain shows gate details

```bash
kardinal explain test-app --env prod
# Expected: shows no-weekend-deploys gate with expression and current value
```

**Pass criteria**: gate name, CEL expression (`!schedule.isWeekend()`), and result visible.

### Scenario 5: Rollback opens a PR

```bash
# Promote first
kardinal create bundle test-app --image $TEST_IMAGE
sleep 60  # wait for test+uat
kardinal rollback test-app --env prod
# Expected: PR opened with kardinal/rollback label and evidence body
```

**Pass criteria**: PR has `kardinal/rollback` label; PR body contains promotion evidence.

### Scenario 6: Concurrent bundles — correct supersession

```bash
IMAGE_A="ghcr.io/pnz1990/kardinal-test-app:sha-aaa1111"
IMAGE_B="ghcr.io/pnz1990/kardinal-test-app:sha-bbb2222"
kardinal create bundle test-app --image $IMAGE_A
sleep 5
kardinal create bundle test-app --image $IMAGE_B
sleep 30
kardinal get pipelines
# Expected: only IMAGE_B bundle is Promoting; IMAGE_A bundle is Superseded
```

**Pass criteria**: older bundle superseded, newer one continues.

### After running scenarios

For each scenario: record PASS/FAIL + actual output.
Open `kind/bug` issue if any scenario fails.
Open `kind/docs` issue if output doesn't match `docs/cli-reference.md`.
Update `docs/aide/definition-of-done.md` journey status table.
Tear down: `make kind-down` (or keep running for continuous validation).
