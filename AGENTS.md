# kardinal-promoter — AI Agent Context

## What This Is

A Kubernetes-native promotion controller built on [kro's Graph primitive](https://github.com/ellistarn/kro/tree/krocodile/experimental). Moves versioned artifact bundles through environment pipelines using Git PRs as the approval mechanism, with policy gates as visible nodes in the promotion DAG.

**Stack**: Go 1.23+ backend + React 19 / Vite / TypeScript frontend, embedded via `go:embed`. All state in Kubernetes CRDs. No external database.

**Status**: Pre-release. Design complete. Implementation not started.

---

## Spec-Driven Development Workflow

This project uses **spec-kit v0.6.0** with the **AIDE extension** for autonomous development.

### AIDE Lifecycle (always run in this order, new session per step)

| Step | Command | Output |
|---|---|---|
| 1 | `/speckit.aide.create-vision` | `docs/aide/vision.md` |
| 2 | `/speckit.aide.create-roadmap` | `docs/aide/roadmap.md` |
| 3 | `/speckit.aide.create-progress` | `docs/aide/progress.md` |
| 4 | `/speckit.aide.create-queue` | `docs/aide/queue/queue-NNN.md` |
| 5 | `/speckit.aide.create-item` | `docs/aide/items/NNN-*.md` |
| 6 | `/speckit.aide.execute-item` | Implementation + progress update |
| 7 | `/speckit.aide.feedback-loop` | Process improvements |

### Feature Implementation Workflow (within a work item)

```
/speckit.specify     → .specify/specs/<NNN-feature>/spec.md
/speckit.plan        → .specify/specs/<NNN-feature>/plan.md
/speckit.tasks       → .specify/specs/<NNN-feature>/tasks.md
/speckit.implement   → Code + tests
```

### SPECIFY_FEATURE

When not on a Git branch, set this env var before running any speckit command:
```bash
export SPECIFY_FEATURE=001-graph-integration
```

---

## Source of Truth (read in this order at session start)

1. **`docs/aide/vision.md`** — product vision, features, architecture, success criteria
2. **`docs/aide/roadmap.md`** — staged delivery plan with acceptance criteria per stage
3. **`docs/aide/progress.md`** — what is done, in progress, planned
4. **`.specify/memory/constitution.md`** — non-negotiable project principles
5. **`.specify/specs/<current-spec>/spec.md`** — current feature requirements
6. **`.specify/specs/<current-spec>/tasks.md`** — current task checklist

Implementation specs (detailed technical design) live in `docs/design/`:
- `01-graph-integration.md` through `09-config-only-promotions.md`

---

## Architecture in One Screen

```
User writes: Pipeline CRD + PolicyGate CRDs
CI creates:  Bundle CRD (via POST /api/v1/bundles)

kardinal-controller:
  Pipeline → generates kro Graph → Graph controller creates PromotionStep + PolicyGate CRs in DAG order
  PromotionStep reconciler → executes steps (git-clone, kustomize-set-image, git-commit, open-pr, health-check)
  PolicyGate reconciler → evaluates CEL, writes status.ready + lastEvaluatedAt

PR opened → human merges → health verified → Verified
On failure → Graph stops downstream → rollback PR opened

All state in etcd. No external DB. kubectl is sufficient.
```

## CRDs (user-facing)

| CRD | Created by | Purpose |
|---|---|---|
| `Pipeline` | User | Promotion topology, environments, Git config |
| `Bundle` | CI / kubectl | Artifact snapshot with provenance |
| `PolicyGate` | Platform team | CEL policy check (template + per-Bundle instance) |
| `PromotionStep` | Graph controller | Per-environment execution state |
| `MetricCheck` | User (Phase 2) | Prometheus query template |
| `Subscription` | User (Phase 3) | Registry/Git watcher |

---

## Go Code Standards (non-negotiable)

- Apache 2.0 copyright header on every `.go` file
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Logging: zerolog via `zerolog.Ctx(ctx)` with structured fields
- Tests: table-driven, `testify/assert` + `require`, `go test -race`
- Commits: Conventional Commits `type(scope): message`
- No `util.go`, no `helpers.go`, no `common.go`
- Every reconciler is idempotent (safe to re-run after crash)
- RBAC: minimum required verbs per CRD resource

---

## Repo Layout

```
cmd/kardinal/               # main.go
internal/
  controller/               # Pipeline, Bundle, PromotionStep, PolicyGate reconcilers
  graph/                    # Graph CRD client and builder (pkg/graph/)
  steps/                    # Promotion step engine (pkg/steps/)
  health/                   # Health adapters (pkg/health/)
  scm/                      # SCM providers (pkg/scm/)
  update/                   # Manifest update strategies (pkg/update/)
web/
  embed.go                  # go:embed all:dist
  src/                      # React 19 frontend
.specify/
  memory/constitution.md    # Non-negotiable principles
  specs/                    # 001-009 feature specs
docs/
  aide/                     # Vision, roadmap, progress, queue, items (AIDE workflow)
  design/                   # Technical implementation specs (01-09)
  quickstart.md             # User documentation
  concepts.md
  cli-reference.md
  pipeline-reference.md
  policy-gates.md
  health-adapters.md
  rollback.md
  ci-integration.md
  troubleshooting.md
examples/
  quickstart/               # 3-env linear pipeline
  multi-cluster-fleet/      # 4-cluster parallel fan-out
```

---

## Key Dependencies

- kro Graph: `kro.run/v1alpha1/Graph` — [experimental branch](https://github.com/ellistarn/kro/tree/krocodile/experimental). Use dynamic client, no compile-time import.
- CEL: `google/cel-go` for PolicyGate expressions
- controller-runtime: Kubernetes reconciler framework
- React 19 + Vite: embedded UI via `go:embed`
- cobra: CLI

---

## Pluggable Interfaces (implementing these = adding providers)

```go
pkg/scm/     → SCMProvider (GitHub, GitLab)
pkg/update/  → Strategy (kustomize, helm, config-merge)
pkg/health/  → Adapter (resource, argocd, flux, argoRollouts, flagger)
pkg/steps/   → Step (git-clone, open-pr, health-check, custom webhook)
pkg/metrics/ → Provider (prometheus, datadog)
pkg/source/  → Watcher (OCI registry, gitCommit)
```

---

## Available Slash Commands

### Core spec-kit
`/speckit.constitution` `/speckit.specify` `/speckit.plan` `/speckit.tasks`
`/speckit.implement` `/speckit.analyze` `/speckit.checklist` `/speckit.clarify`

### AIDE workflow
`/speckit.aide.create-vision` `/speckit.aide.create-roadmap` `/speckit.aide.create-progress`
`/speckit.aide.create-queue` `/speckit.aide.create-item` `/speckit.aide.execute-item`
`/speckit.aide.feedback-loop`

### Quality and validation
`/speckit.verify-tasks.run`  — detect phantom completions (tasks marked [x] with no real code)
`/speckit.memorylint.run`    — audit conflicts between AGENTS.md and constitution
`/speckit.doctor.check`      — full project health diagnostic
`/speckit.status.show`       — current SDD workflow progress

### Git workflow
`/speckit.git.feature`       — create feature branch + spec directory
`/speckit.git.commit`        — auto-commit outstanding changes
`/speckit.git.validate`      — validate branch state

### Worktree (parallel development)
`/speckit.worktree.create`   — spawn isolated worktree for a feature
`/speckit.worktree.list`     — list active worktrees with spec status
`/speckit.worktree.clean`    — remove stale/merged worktrees

---

## Anti-Patterns (DO NOT repeat)

| Anti-pattern | Why |
|---|---|
| Mutating Deployments/Services directly | Violates P8 (never mutate workload resources) |
| Building a DAG engine from scratch | kro Graph handles this |
| Adding an external database | Kubernetes etcd is the database |
| Implementing Cedar or OPA | CEL is the only policy language |
| Marking tasks [x] before writing code | Will be caught by `/speckit.verify-tasks.run` |
| Copy-pasting CEL evaluation logic | Single evaluator in `pkg/reconciler/policygate/evaluator.go` |
| Hardcoded provider names | All providers registered via interface registry |

---

## Testing Requirements

- Unit tests for every reconciler, step, adapter, and translator function
- Integration tests require running Graph controller (envtest or kind)
- E2E: kind cluster + Graph controller + GitHub repo + 3-env pipeline + PolicyGate blocking
- `go test -race` must pass
- Run `/speckit.verify-tasks.run` after every implement session
