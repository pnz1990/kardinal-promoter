# kardinal-promoter — AI Agent Context

## What This Is

A Kubernetes-native promotion controller built on [kro's Graph primitive](https://github.com/ellistarn/kro/tree/krocodile/experimental). Moves versioned artifact bundles through environment pipelines using Git PRs as the approval mechanism, with policy gates as visible nodes in the promotion DAG.

**Stack**: Go 1.23+ backend + React 19 / Vite / TypeScript frontend, embedded via `go:embed`. All state in Kubernetes CRDs. No external database.

**Status**: Pre-release. Design and specs complete. Implementation not started.

---

## Autonomous Development Mode (NON-NEGOTIABLE)

This project is built 100% by autonomous AI agents. No human writes code. Agents work in parallel in isolated git worktrees, push PRs, review each other's work, and validate against user documentation and tests.

**The coordination model**: `docs/aide/team.yml` is the team config. Read it. Know your role.

**The feedback loop**:
```
Human: defines vision, roadmap, unblocks escalations
  └── Coordinator agent: reads queue, assigns items, tracks state
        ├── Feature agent A: implements spec in worktree, pushes PR
        ├── Feature agent B: implements spec in worktree, pushes PR
        └── QA agent: reviews PRs against spec + docs + tests
```

---

## Reading Order at Session Start

Every agent reads these in order before doing anything:

1. `docs/aide/vision.md` — product vision, features, success criteria
2. `docs/aide/roadmap.md` — staged delivery, acceptance criteria per stage
3. `docs/aide/progress.md` — what is done, in progress, planned
4. `.specify/memory/constitution.md` — non-negotiable principles (overrides everything)
5. `docs/aide/team.yml` — your role, responsibilities, rules
6. `AGENTS.md` — this file (architecture, commands, standards)
7. **Your assigned item**: `docs/aide/items/<NNN-item>.md` or `.specify/specs/<feature>/`

---

## Role-Specific Instructions

### If you are the COORDINATOR

Run: `/speckit.maqa.coordinator`

1. Read `docs/aide/progress.md` and the latest `docs/aide/queue/queue-NNN.md`
2. Read `.maqa/state.json` for current feature states
3. Read `maqa-config.yml` for max_parallel (3) and test commands
4. Assign `todo` items to feature agents (up to max_parallel concurrent)
5. Gate on CI before spawning QA (`maqa-ci` extension)
6. After QA approves + CI green + PR merged: update `.maqa/state.json` to `done`
7. When queue is exhausted: update `docs/aide/progress.md`

Never implement features. Never commit. Return SPAWN blocks only.

### If you are a FEATURE AGENT

Run: `/speckit.maqa.feature`

Your assignment comes from the coordinator. Read your item in `docs/aide/items/`.

**Work backwards from these in order:**
1. `docs/aide/items/<NNN-item>.md` — what to build
2. `.specify/specs/<feature>/spec.md` — acceptance criteria (Given/When/Then)
3. `docs/design/<feature>.md` — technical implementation spec (interfaces, algorithms)
4. `docs/quickstart.md` + `docs/concepts.md` — user docs are acceptance criteria
5. `examples/` — working YAML is a contract; your code must produce this behavior

**TDD cycle** (tdd: true in maqa-config.yml):
```
Write failing test → Implement → go test ./... -race green → Next task
```

Before marking done:
- `go test ./... -race` must pass
- `go vet ./...` must pass
- Run `/speckit.verify-tasks.run` (no phantom completions)
- Push branch (`auto_push: true` in maqa-config.yml)
- Open PR using `docs/aide/pr-template.md`

### If you are the QA AGENT

Run: `/speckit.maqa.qa`

You review the feature PR. You do NOT re-run tests (CI does that). You do:

1. Read the spec: `.specify/specs/<feature>/spec.md` — check every acceptance scenario
2. Read the design doc: `docs/design/<feature>.md` — check all edge cases are handled
3. Read the PR diff — check against constitution anti-patterns
4. Check user docs consistency: `docs/` — does the implementation match what users expect?
5. Check examples: `examples/` — do the YAML examples still produce correct behavior?
6. Check Go standards (from constitution):
   - Apache 2.0 on every .go file
   - No util.go, helpers.go, common.go
   - `fmt.Errorf("context: %w", err)` for error wrapping
   - Idempotency tests for every reconciler
7. If QA passes: approve PR
8. If QA fails: request changes with specific file:line references

---

## Architecture

```
User writes: Pipeline CRD + PolicyGate CRDs
CI creates:  Bundle CRD (via POST /api/v1/bundles)

kardinal-controller:
  1. Pipeline + Bundle → generates kro Graph (per-Bundle, tailored to intent)
  2. Graph controller creates PromotionStep + PolicyGate CRs in DAG order
  3. PromotionStep reconciler executes steps:
       git-clone → kustomize-set-image → git-commit → open-pr → wait-for-merge → health-check
  4. PolicyGate reconciler evaluates CEL → writes status.ready + lastEvaluatedAt
  5. Graph advances on readyWhen satisfied
  6. On failure: Graph stops downstream → rollback PR opened

All state in etcd. No external DB. kubectl is sufficient.
```

## CRDs

| CRD | Created by | Purpose |
|---|---|---|
| `Pipeline` | User | Promotion topology, environments, Git config |
| `Bundle` | CI / kubectl | Artifact snapshot with provenance and intent |
| `PolicyGate` | Platform team | CEL policy check (template + per-Bundle instance) |
| `PromotionStep` | Graph controller | Per-environment execution state (DO NOT create manually) |
| `MetricCheck` | User (Phase 2) | Prometheus query template |
| `Subscription` | User (Phase 3) | Registry/Git watcher |

## Repo Layout

```
cmd/kardinal/               # main.go
internal/
  controller/               # Pipeline, Bundle reconcilers
pkg/
  graph/                    # Graph CRD client and builder
  reconciler/
    promotionstep/          # PromotionStep state machine
    policygate/             # CEL evaluator + timer recheck
  steps/                    # Promotion step engine (git-clone, open-pr, etc.)
  health/                   # Health adapters (Deployment, Argo CD, Flux)
  scm/                      # SCM providers (GitHub, GitLab)
  update/                   # Manifest update strategies (kustomize, helm)
web/
  embed.go                  # go:embed all:dist
  src/                      # React 19 frontend
.specify/
  memory/constitution.md    # Non-negotiable principles
  specs/                    # 001-009 feature specs
  templates/overrides/      # Go+Kubernetes spec/tasks templates
docs/
  aide/                     # Vision, roadmap, progress, queue, items, team.yml
  design/                   # Technical implementation specs (01-09)
  quickstart.md             # User documentation (acceptance criteria)
  concepts.md
examples/
  quickstart/               # 3-env linear pipeline (acceptance test)
  multi-cluster-fleet/      # 4-cluster parallel fan-out (acceptance test)
.maqa/
  state.json                # MAQA coordinator state (feature lifecycle tracking)
maqa-config.yml             # MAQA team configuration (test command, parallelism, etc.)
```

## Go Standards (CONSTITUTION — non-negotiable)

- Apache 2.0 copyright header on every `.go` file
- `fmt.Errorf("context: %w", err)` for error wrapping
- zerolog via `zerolog.Ctx(ctx)` with structured fields
- Table-driven tests, `testify/assert` + `require`, `go test -race`
- Commits: Conventional Commits `type(scope): message`
- **No** `util.go`, `helpers.go`, `common.go`
- Every reconciler is idempotent (safe to re-run after crash)

## Pluggable Interfaces (implementing these = adding providers)

```go
pkg/scm/     → SCMProvider (GitHub Phase 1, GitLab Phase 2)
pkg/update/  → Strategy (kustomize Phase 1, helm Phase 2, config-merge Phase 2)
pkg/health/  → Adapter (resource, argocd, flux Phase 1; argoRollouts, flagger Phase 2)
pkg/steps/   → Step (10 built-in + custom webhooks)
pkg/metrics/ → Provider (prometheus Phase 2)
pkg/source/  → Watcher (OCI registry, gitCommit Phase 3)
```

## SPECIFY_FEATURE

When not on a Git branch (e.g., autonomous session without git checkout):
```bash
export SPECIFY_FEATURE=001-graph-integration
```
This tells spec-kit which feature directory to use for plan/tasks/implement.

## Anti-Patterns (will be caught by QA and verify-tasks)

| Anti-pattern | Caught by |
|---|---|
| Mutating Deployments/Services directly | QA constitution check |
| Building a DAG engine (Graph does this) | QA spec check |
| Adding external database | QA constitution check |
| Using Cedar or OPA instead of CEL | QA spec check |
| Marking tasks [x] before writing code | `/speckit.verify-tasks.run` |
| Copy-pasting CEL evaluation logic | QA code review |
| No idempotency test on reconciler | QA checklist |
| Missing Apache 2.0 header | QA go_qa check |
| Creating util.go or helpers.go | QA go_qa check |
| Not using `fmt.Errorf("context: %w", err)` | QA go_qa check |

## All Available Commands

### AIDE workflow (human runs these)
`/speckit.aide.create-vision`
`/speckit.aide.create-roadmap`
`/speckit.aide.create-progress`
`/speckit.aide.create-queue`
`/speckit.aide.create-item`
`/speckit.aide.execute-item`
`/speckit.aide.feedback-loop`

### MAQA multi-agent (coordinator/agents run these)
`/speckit.maqa.setup`         — one-time setup (not needed for OpenCode)
`/speckit.maqa.coordinator`   — COORDINATOR: orchestrate feature + QA agents
`/speckit.maqa.feature`       — FEATURE AGENT: implement one item in worktree
`/speckit.maqa.qa`            — QA AGENT: review PR against spec + docs + tests

### Core spec-kit (feature agents run these within an item)
`/speckit.specify`
`/speckit.plan`
`/speckit.tasks`
`/speckit.implement`
`/speckit.analyze`
`/speckit.checklist`
`/speckit.clarify`

### Quality gates (QA agent + feature agents run these)
`/speckit.verify-tasks.run`   — detect phantom completions
`/speckit.memorylint.run`     — audit AGENTS.md vs constitution
`/speckit.doctor.check`       — full project health diagnostic
`/speckit.status.show`        — current SDD workflow progress

### Worktree (coordinator runs these)
`/speckit.worktree.create`    — spawn isolated worktree for a feature branch
`/speckit.worktree.list`      — list active worktrees with feature status
`/speckit.worktree.clean`     — remove stale/merged worktrees

### Git (feature agents run these)
`/speckit.git.feature`        — create feature branch + spec directory
`/speckit.git.commit`         — auto-commit outstanding changes
`/speckit.git.validate`       — validate branch state

### Release (human or coordinator runs)
`/speckit.ship`               — automates release pipeline: pre-flight, changelog, CI verify, PR
