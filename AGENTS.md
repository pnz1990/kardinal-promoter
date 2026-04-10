# kardinal-promoter — AI Agent Context

## What This Is

A Kubernetes-native promotion controller. Go 1.23+ backend + React 19 frontend, embedded via `go:embed`. All state in Kubernetes CRDs. No external database.

**Status**: Pre-release. Design and specs complete. Implementation not started.

---

## Autonomous Development Model

This project is built 100% by autonomous AI agents. No human writes code. The loop is:

```
Human:       defines vision + roadmap → checks GitHub Project board → unblocks escalations
Coordinator: generates queue → creates items → assigns to feature agents → gates on CI → spawns QA
Feature:     TDD in isolated worktree → verifies against spec + docs → pushes PR
QA:          reviews PR against spec + user docs + examples → approves or rejects
Board:       GitHub Projects (https://github.com/users/pnz1990/projects/1)
```

The human never creates queue files, item files, or specs. The coordinator does all of that by reading `docs/aide/progress.md` and `docs/aide/roadmap.md`.

The implementation **works backwards** from user documentation and examples:
- `docs/quickstart.md` and `docs/concepts.md` define what the system must do
- `examples/quickstart/` and `examples/multi-cluster-fleet/` are acceptance tests
- If a feature is not in the user docs, it does not exist

---

## Reading Order (every agent, every session)

Read these **in order** before doing anything else:

1. `docs/aide/vision.md` — product vision, features, success criteria
2. `docs/aide/roadmap.md` — staged delivery, acceptance criteria per stage
3. `docs/aide/progress.md` — what is done / in progress / planned
4. `.specify/memory/constitution.md` — non-negotiable principles (overrides everything)
5. `docs/aide/team.yml` — your role, rules, lifecycle
6. **Your assigned item**: `docs/aide/items/<NNN>.md` or `.specify/specs/<feature>/`

---

## Role-Specific Instructions

### COORDINATOR — `/speckit.maqa.coordinator`

1. Read `docs/aide/progress.md` + `docs/aide/roadmap.md` to understand what stage to work on next
2. If `docs/aide/queue/` is empty: generate the next batch of work items by running `/speckit.aide.create-queue`, then `/speckit.aide.create-item` for each item
3. Populate the GitHub Projects board via `/speckit.maqa-github-projects.populate`
4. Read `.maqa/state.json` for current feature states
5. Assign `todo` items to feature agents respecting dependency order (max 3 parallel from `maqa-config.yml`)
6. After feature agent completes: check CI via `/speckit.maqa-ci.check`
7. If CI green: move to In Review, spawn QA agent via `/speckit.maqa.qa`
8. If QA approves + PR merged: update `.maqa/state.json` → `done`, move card on GitHub Projects
9. When queue exhausted: update `docs/aide/progress.md`, generate next queue

**Never implement features. Never commit. Return SPAWN blocks only.**
**The human does not create queue files or item files. You do.**

### FEATURE AGENT — `/speckit.maqa.feature`

Work backwards in this exact order:
1. `docs/aide/items/<assigned>.md` — what to build
2. `.specify/specs/<feature>/spec.md` — acceptance criteria (Given/When/Then)
3. `.specify/specs/<feature>/tasks.md` — ordered task checklist
4. `docs/design/<feature>.md` — technical implementation spec
5. `docs/quickstart.md` + `docs/concepts.md` — user docs = acceptance criteria
6. `examples/` — working YAML = contract; your code must produce this behavior

**TDD cycle** (`tdd: true` in `maqa-config.yml`):
```
Write failing test → Implement → go test ./... -race green → tick task → next task
```

**Done criteria** (run all before opening PR):
```bash
go test ./... -race          # must pass
go vet ./...                 # zero findings
/speckit.verify-tasks.run    # no phantom completions
/speckit.verify              # spec acceptance criteria met
```

Then open PR using `docs/aide/pr-template.md`. Branch is auto-pushed (`auto_push: true`).

**Unblocking rules:**
- If stuck on a dependency (spec not done): move item back to `backlog`, label issue `blocked`, notify coordinator
- If stuck on ambiguity in spec: label issue `needs-human`, stop, notify
- If CI is flaky (not your code): retry once, then label `needs-human`
- Never wait more than 2 retries before escalating

### QA AGENT — `/speckit.maqa.qa`

Review the PR. You do NOT re-run tests (CI does that). You check:

1. Every Given/When/Then from `.specify/specs/<feature>/spec.md` is satisfied
2. Every FR-NNN from the spec has an implementation
3. PR diff is consistent with `docs/design/<feature>.md` edge cases
4. User docs (`docs/`) match what was built (if user-facing behavior changed)
5. `examples/` YAML still produces correct behavior
6. Go standards (from constitution — check every .go file in the diff):
   - Apache 2.0 header present
   - No `util.go`, `helpers.go`, `common.go`
   - `fmt.Errorf("context: %w", err)` for error wrapping
   - Idempotency test for every reconciler
7. No kro module in `go.mod` (dynamic client only)

**Approve** if all pass. **Request changes** if any fail — provide exact `file:line` references.

---

## Architecture

```
User writes: Pipeline CRD + PolicyGate CRDs
CI creates:  Bundle CRD (via POST /api/v1/bundles)

kardinal-controller:
  1. Bundle → translator generates kro Graph (per-Bundle, one per Pipeline)
  2. Graph controller creates PromotionStep + PolicyGate CRs in DAG order
  3. PromotionStep reconciler: steps → git-clone → kustomize-set-image →
                                git-commit → open-pr → wait-for-merge → health-check
  4. PolicyGate reconciler: evaluates CEL → status.ready + lastEvaluatedAt
  5. Graph advances on readyWhen satisfied
  6. Failure → Graph stops downstream → rollback PR opened

Everything is a CRD. kubectl is sufficient to operate the system.
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

## Go Standards (CONSTITUTION — every .go file)

```
// Copyright [year] The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```
- `fmt.Errorf("context: %w", err)` — no bare errors
- zerolog via `zerolog.Ctx(ctx)` — no fmt.Println
- Table-driven tests, `testify/assert` + `require`, `go test -race`
- `Conventional Commits`: `feat(scope): desc`, `fix(scope): desc`, `test(scope): desc`
- No `util.go`, `helpers.go`, `common.go`
- Every reconciler: idempotent, safe to re-run after crash

## Active Commands (the only ones agents need)

### AIDE (human-triggered only for vision/roadmap changes)
```
/speckit.aide.feedback-loop     reflect, adjust vision/roadmap when something is wrong
```

### AIDE (coordinator runs these, not the human)
```
/speckit.aide.create-queue      generate next 10 work items from progress + roadmap
/speckit.aide.create-item       create detailed spec for a queue item
```

### MAQA (agent-driven)
```
/speckit.maqa.coordinator       orchestrate: assign items, gate CI, spawn QA
/speckit.maqa.feature           implement one item in isolated worktree
/speckit.maqa.qa                review PR against spec + docs + examples
/speckit.maqa-ci.check          check GitHub Actions CI status on branch
/speckit.maqa-github-projects.setup     one-time: bootstrap GitHub Projects config
/speckit.maqa-github-projects.populate sync specs → GitHub Project board
```

### Quality gates (feature agent runs before every PR)
```
/speckit.verify-tasks.run       detect phantom completions
/speckit.verify                 validate spec acceptance criteria
```

### Worktree (coordinator runs)
```
/speckit.worktree.create        spawn isolated worktree for feature branch
/speckit.worktree.list          list active worktrees
/speckit.worktree.clean         remove stale/merged worktrees
```

### Git (feature agent runs)
```
/speckit.git.feature            create feature branch + spec directory
/speckit.git.commit             auto-commit changes
```

## Escalation Protocol

When an agent cannot proceed, it MUST:

1. Label the GitHub Issue with `blocked` or `needs-human`
2. Write a comment explaining exactly what is blocking (file, line, question)
3. Set item state to `blocked` in `.maqa/state.json`
4. Stop. Do not attempt workarounds.

The human reads the label, resolves it, removes the label, and the coordinator resumes.

**Max retries before escalation: 2** (configured in `docs/aide/team.yml`)

## Anti-Patterns (QA blocks PRs containing these)

| Pattern | Detected by |
|---|---|
| Task `[x]` without implementation | `/speckit.verify-tasks.run` |
| Mutating Deployments/Services directly | `/speckit.verify` (spec check) |
| `import "github.com/ellistarn/kro/..."` in go.mod | QA review |
| Missing Apache 2.0 header | QA review |
| `util.go` / `helpers.go` / `common.go` | QA review |
| CEL evaluation copy-pasted | QA review (single evaluator in `pkg/cel/`) |
| No idempotency test on reconciler | QA review |
| Feature not in user docs | `/speckit.verify` (spec check) |

---

## SPECIFY_FEATURE

When running spec-kit commands outside a git branch context:
```bash
export SPECIFY_FEATURE=001-graph-integration
```

## Files Agents Must Not Modify

- `docs/aide/vision.md` — human territory
- `docs/aide/roadmap.md` — human territory
- `AGENTS.md` — human territory
- `.specify/memory/constitution.md` — human territory
- `docs/aide/team.yml` — human territory
