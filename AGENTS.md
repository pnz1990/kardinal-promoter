# kardinal-promoter — Project Agent Context

## What This Is

A Kubernetes-native promotion controller. Go 1.23+ backend + React 19 frontend,
embedded via `go:embed`. All state in Kubernetes CRDs. No external database.

**Status**: Pre-release. Design and specs complete. Implementation not started.

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

PM must NOT know about: SDLC process, team.yml, sdlc.md, templates, maqa-config.

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
| Task `[x]` without implementation | `/speckit.verify-tasks.run` |
| Mutating Deployments/Services directly | `/speckit.verify` |
| kro import in go.mod | CI + QA |
| Missing Apache 2.0 header | CI + QA |
| Banned filenames | CI + QA |
| No idempotency test on reconciler | QA |
| Feature not in user docs | `/speckit.verify` |
| go.mod not tidy | CI |
| **Business logic evaluated outside a Graph node or reconciler that writes to CRD status** | **QA — Graph-first violation → NEEDS HUMAN** |
| **New usage of `pkg/cel` outside `pkg/reconciler/policygate`** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Reconciler that makes decisions based on fields not written to its own CRD status** | **QA — Graph-first violation → NEEDS HUMAN** |
| **CEL FunctionBinding that makes HTTP calls or external I/O** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Dependency between components expressed as in-memory state, not CRD fields** | **QA — Graph-first violation → NEEDS HUMAN** |
| **Bypassing Graph for "simple" promotion cases** | **QA — Graph-first violation → NEEDS HUMAN** |

## Graph-First Architecture (read before implementing any new feature)

The world is a DAG. Everything is a Graph node. See `docs/design/10-graph-first-architecture.md`.

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
