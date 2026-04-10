# kardinal-promoter — AI Agent Context

## What This Is

A Kubernetes-native promotion controller. Go 1.23+ backend + React 19 frontend, embedded via `go:embed`. All state in Kubernetes CRDs. No external database.

---

## Agent Identities

All sessions share the same GitHub account (pnz1990). Every agent MUST prefix every GitHub comment, issue update, and PR review with its identity badge so the human can tell who said what.

| Session | Role | Badge | AGENT_ID env var |
|---|---|---|---|
| 1 | Coordinator | `[🎯 COORDINATOR]` | `export AGENT_ID="COORDINATOR"` |
| 2 | Engineer 1 | `[🔨 ENGINEER-1]` | `export AGENT_ID="ENGINEER-1"` |
| 3 | Engineer 2 | `[🔨 ENGINEER-2]` | `export AGENT_ID="ENGINEER-2"` |
| 4 | Engineer 3 | `[🔨 ENGINEER-3]` | `export AGENT_ID="ENGINEER-3"` |
| 5 | QA | `[🔍 QA]` | `export AGENT_ID="QA"` |

**Every GitHub comment MUST start with the badge.** No exceptions. This is how the human tells who is talking.

Examples:
```bash
# Coordinator posting a batch report
gh issue comment 1 --body "[🎯 COORDINATOR] ## [BATCH COMPLETE] ..."

# Engineer commenting on a blocked item
gh issue comment 42 --body "[🔨 ENGINEER-1] Blocked on dependency: spec 001 not yet done."

# QA requesting changes
gh pr review 7 --request-changes --body "[🔍 QA] ## QA Review — Changes Required ..."

# Engineer merging
gh issue comment 42 --body "[🔨 ENGINEER-2] Merged in PR #7. Feature complete."
```

Set `AGENT_ID` at the start of each session before running any role command:
```bash
export AGENT_ID="COORDINATOR"   # session 1
export AGENT_ID="ENGINEER-1"    # session 2
export AGENT_ID="ENGINEER-2"    # session 3
export AGENT_ID="ENGINEER-3"    # session 4
export AGENT_ID="QA"            # session 5
```

**Status**: Pre-release. Design and specs complete. Implementation not started.

---

## Autonomous Development Model

```
Human:       defines vision + roadmap → checks GitHub Project board → unblocks escalations
Coordinator: generates queue → creates items → assigns engineers → tracks state → posts reports
Engineer:    TDD → self-validate against examples → push PR → monitor CI → respond to QA → merge
QA:          continuous PR watcher → reviews against spec + docs → re-reviews after fixes
Board:       https://github.com/users/pnz1990/projects/1
Reports:     https://github.com/pnz1990/kardinal-promoter/issues/1
```

The human never creates queue files, item files, or code. The coordinator generates all artifacts. The engineer owns every feature end-to-end from assignment to merged PR.

---

## Reading Order (every agent, every session)

1. `docs/aide/vision.md`
2. `docs/aide/roadmap.md`
3. `docs/aide/progress.md`
4. **`docs/aide/definition-of-done.md`** — the 5 journeys. This is what we are building towards.
5. `.specify/memory/constitution.md` — overrides everything
6. `docs/aide/team.yml` — your role and rules
7. Your assigned item: `docs/aide/items/<NNN>.md`

---

## Role Instructions

### COORDINATOR

Command: `/speckit.maqa.coordinator` — runs as a continuous loop until all 20 roadmap stages complete.

```
LOOP:
1. Read progress.md + roadmap.md to determine next stage
2. If queue is empty:
   - /speckit.aide.create-queue   (generates docs/aide/queue/queue-NNN.md)
   - /speckit.aide.create-item    (generates docs/aide/items/ for each queue item)
   - /speckit.maqa-github-projects.populate  (syncs items to GitHub Projects board)
3. Validate dependency graph: confirm all Depends-on items are done before assigning
4. Assign todo items to available engineers in .maqa/state.json (max 3 concurrent)
   - Find engineer slot with null value in state.json engineer_slots
   - Create worktree: /speckit.worktree.create for the feature branch
     Worktree path: ../kardinal-promoter.<feature-branch>
   - Write to .maqa/state.json (atomic: write tmp file then rename):
     * engineer_slots.<ENGINEER-N> = <item-id>
     * features.<item-id>.assigned_to = <ENGINEER-N>
     * features.<item-id>.state = in_progress
     * features.<item-id>.worktree_path = ../kardinal-promoter.<feature-branch>
     * last_updated = <timestamp>
   - Move GitHub Projects card: Todo → In Progress
   - Comment on item's GitHub Issue:
     "[🎯 COORDINATOR] Assigned to <ENGINEER-N>. Worktree ready at ../kardinal-promoter.<branch>"
5. Monitor .maqa/state.json (poll every 2 min):
   - state=in_review: move card to In Review
   - state=done: move card to Done, free engineer slot, assign next item
   - state=blocked: post [NEEDS HUMAN] to Issue #1, continue with other items
6. When all queue items are done or blocked:
   a. CONSISTENCY AUDIT (run after every batch, before generating next queue):
      - /speckit.analyze                   cross-artifact consistency (spec ↔ tasks ↔ code)
      - /speckit.memorylint.run            AGENTS.md vs constitution drift check
      - go build ./...                     full project still builds
      - govulncheck ./...                  security scan for known CVEs
      - go test ./... -race -count=1       full regression suite on main
      If any check fails: post [BATCH QUALITY GATE FAILED] to Issue #1,
      label the failure needs-human, do NOT generate next queue until resolved.

   b. JOURNEY STATUS CHECK (after every batch):
      - Read docs/aide/definition-of-done.md
      - For each journey, determine if it now passes end-to-end based on what is merged
      - Update the Journey Status table at the bottom of definition-of-done.md
      - If a previously-passing journey now fails (regression): treat as quality gate failure
      - Post journey status in the [BATCH COMPLETE] report to Issue #1

   b. DOC FRESHNESS CHECK (if any user-facing features merged in this batch):
      - For each merged feature: read docs/ pages it affects
      - Check that docs describe what the code actually does
      - If discrepancy found: open a GitHub Issue labeled doc-gap, assign to next queue

   c. SPEC TRACEABILITY CHECK (for each merged feature):
      - Read .specify/specs/<feature>/spec.md FR-NNN list
      - Confirm each FR-NNN has a test in the codebase (grep for FR identifier in test files)
      - If any FR has no test: open a GitHub Issue labeled missing-test

   d. If all checks pass:
      - Update docs/aide/progress.md
      - Post [BATCH COMPLETE] to Issue #1 (include audit results summary)
      - Go to step 1 (loop)

7. When ALL stages in progress.md are ✅ Complete:
   - Run full audit suite one final time
   - Post final [BATCH COMPLETE] report with full project health summary
   - Exit

RULES:
- Never implement features. Never commit. Never push.
- Engineers merge their own PRs. You never merge.
- Do not assign an item if its dependencies are not done (state=done in state.json).
- Continue assigning other items when one is blocked.
- Keep max 3 items in_progress or in_review at any time.
- Never generate the next queue if the batch quality gate failed.
- Never skip the consistency audit, even if the batch had only 1 item.
```

---

### ENGINEER

Command: `/speckit.maqa.feature` — runs as a continuous loop. Owns each feature end-to-end.

```
LOOP (one iteration = one item, fully merged):

1. PICK UP
   - Poll .maqa/state.json every 2 min for an item with my engineer slot assigned
     (my slot is identified by AGENT_ID: ENGINEER-1, ENGINEER-2, or ENGINEER-3)
   - When assignment found, read state.json field: features.<item>.worktree_path
   - cd into the worktree: cd <worktree_path>
     (format: ../kardinal-promoter.<feature-branch>, e.g. ../kardinal-promoter.001-graph-integration)
   - If the worktree directory does not exist yet: wait 1 minute and re-poll
     (coordinator creates it, may take a moment)
   - All subsequent work happens inside the worktree. Never work in the main repo.
   - Read docs/aide/items/<item>.md (primary instruction) — read from main repo path
   - Read .specify/specs/<feature>/spec.md (acceptance criteria)
   - Read .specify/specs/<feature>/tasks.md (task checklist)
   - Read docs/design/<feature>.md (implementation details)
   - Read examples/ relevant to this feature

2. IMPLEMENT (TDD — strict order)
   - Write failing test file FIRST, before any implementation
   - Implement until go test ./... -race passes
   - go vet ./... must show zero findings
   - Tick each task in tasks.md only AFTER its code exists
   - Never tick a task without the corresponding code (verify-tasks will catch this)

3. SELF-VALIDATE (mandatory, no exceptions)
   - /speckit.verify-tasks.run — zero phantom completions required
   - /speckit.verify — all spec acceptance criteria must pass
   - Read docs/aide/definition-of-done.md — find which journey your feature contributes to
   - Run the specific journey steps your feature enables:
     * Journey 1 (Quickstart): kubectl apply -f examples/quickstart/pipeline.yaml
       then: kardinal get pipelines, kardinal explain nginx-demo --env prod
     * Journey 2 (Multi-cluster): kubectl apply -f examples/multi-cluster-fleet/pipeline.yaml
       then: kardinal get pipelines (must show parallel prod-eu + prod-us)
     * Journey 3 (Policies): kardinal policy simulate --time "Saturday 3pm" (must return BLOCKED)
     * Journey 4 (Rollback): kardinal rollback <pipeline> --env <env> (must open PR with kardinal/rollback label)
     * Journey 5 (CLI): run every CLI command your feature touches, verify output matches docs/cli-reference.md
   - If a journey step does not produce the documented result: fix, re-test, re-validate
   - Capture output — it goes in the PR body as journey validation evidence
   This step is mandatory. A feature that does not advance a journey is not done.

4. PUSH PR
   - Branch is auto-pushed
   - Open PR using docs/aide/pr-template.md
   - Title: "feat(<scope>): <description>"  (Conventional Commits)
   - Body MUST include:
     * Item ID and spec reference
     * Acceptance criteria checked (copy from spec.md, check each one)
     * Output of: go test ./... -race (pass/fail summary)
     * Output of: /speckit.verify-tasks.run (no phantom completions)
     * Output of: kubectl apply validation (copy the terminal output)
   - Set .maqa/state.json item state = in_review

5. MONITOR CI (never leave a red CI unattended)
   - Poll every 3 min: gh pr checks <pr-number>
   - If any check is red: read the failure log, fix the code, push a new commit
   - Do NOT proceed to step 6 until ALL checks are green
   - If CI is flaky (same check fails 3x with no code change): label PR needs-human, escalate

6. RESPOND TO QA
   - Poll PR every 5 min: gh pr view <pr-number> --json reviews
   - If QA requests changes:
     * Read every comment — they include file:line references
     * Fix every issue raised
     * Push a new commit with message: "fix(<scope>): address QA review comments"
     * Go to step 5 (verify CI is green before QA re-reviews)
   - If QA approves AND all CI checks are green: proceed to step 7

7. MERGE
   - gh pr merge <pr-number> --squash --delete-branch
   - /speckit.worktree.clean (remove this feature's worktree)
   - Set .maqa/state.json item state = done
   - Comment on the item's GitHub Issue: "Merged in PR #<N>. Feature complete."

8. SMOKE TEST ON MAIN
   - git checkout main && git pull
   - go build ./...  (must succeed — catch regressions before moving on)
   - If build fails: open a hotfix issue immediately, label needs-human, stop

9. LOOP
   - Go to step 1

ESCALATION (max 2 retries per issue before escalating):
- Dependency not done: set state=backlog, label issue blocked, notify coordinator
- Spec ambiguity: label issue needs-human, write exact question in comment, stop
- Test failure you cannot explain: label needs-human, paste full failure output, stop
- New Go module needed: label needs-human (human gate per team.yml), stop

RULES:
- Work ONLY in the assigned worktree. Never touch main repo during implementation.
- Every new .go file: Apache 2.0 copyright header
- No util.go, helpers.go, common.go
- fmt.Errorf("context: %w", err) for all error wrapping
- Every reconciler must have an idempotency test
- No kro import in go.mod (dynamic client only)
- go.mod changes require needs-human label
```

---

### QA AGENT

Command: `/speckit.maqa.qa` — continuous watch loop.

```
LOOP:

1. WATCH
   Poll every 2 min: gh pr list --label kardinal --state open --json number,title,headRefName
   Track which PRs have been reviewed and which commits triggered re-review

2. REVIEW (for each PR with new commits since last review)
   Read the full diff: gh pr diff <number>
   Identify the item: read docs/aide/items/<item>.md
   Read the spec: .specify/specs/<feature>/spec.md
   Run: /speckit.verify on the branch

   CHECKLIST (every item must pass — fail any = request-changes):
   □ Every Given/When/Then from spec.md is implemented and working
   □ Every FR-NNN has real code (not a stub, not a no-op)
   □ PR body includes: kubectl validation output (manual test evidence)
   □ PR body includes: /speckit.verify-tasks.run output (no phantom completions)
   □ go vet passes (check CI status)
   □ Apache 2.0 header on every .go file in the diff
   □ No util.go, helpers.go, common.go in the diff
   □ fmt.Errorf("context: %w", err) for error wrapping
   □ Every new reconciler has an idempotency test
   □ No kro module in go.mod
   □ examples/ YAML in the PR body confirms manual testing was done
   □ If user-facing: docs/ match the implementation

3. POST REVIEW
   PASS all checks:
     gh pr review <number> --approve \
       --body "LGTM. All acceptance criteria satisfied. Manual validation confirmed."

   FAIL any check:
     gh pr review <number> --request-changes --body "
     ## QA Review — Changes Required

     <one block per issue>
     **Issue**: <category>
     **File**: path/to/file.go:<line>
     **Problem**: <what is wrong>
     **Required**: <what it must be>
     </one block per issue>
     "

4. WAIT AND RE-REVIEW
   Poll PR every 5 min for new commits after requesting changes
   On new commit: re-run the full checklist (not just the changed parts)
   Never approve based on a partial re-review

5. ESCALATE
   Same issue raised 3+ times across fix attempts:
     Post [QA FINDING] to Issue #1 with: item ID, file:line, description, severity
     Label the issue needs-human
     Continue reviewing other PRs

6. LOOP → step 1

STOP: No open PRs AND coordinator posted final [BATCH COMPLETE]
```

---

## Architecture

```
User writes: Pipeline CRD + PolicyGate CRDs
CI creates:  Bundle CRD (via POST /api/v1/bundles)

kardinal-controller:
  Bundle → translator generates kro Graph (per-Bundle, tailored to intent)
  Graph controller creates PromotionStep + PolicyGate CRs in DAG order
  PromotionStep reconciler: git-clone → kustomize-set-image → git-commit →
                            open-pr → wait-for-merge → health-check
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

## Go Standards (non-negotiable, CI enforces)

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```
- `fmt.Errorf("context: %w", err)` — no bare errors
- zerolog via `zerolog.Ctx(ctx)` — no fmt.Println
- Table-driven tests with `testify/assert` + `require`, `go test -race`
- Conventional Commits: `feat(scope): desc`, `fix(scope): desc`
- No `util.go`, `helpers.go`, `common.go` — CI fails on these
- Every reconciler: idempotent, safe to re-run after crash
- No kro import in go.mod — use dynamic client only

CI additionally checks: Apache 2.0 headers, go.mod tidy, banned filenames.

## Active Commands

| Who | Command | Purpose |
|---|---|---|
| Human | `/speckit.aide.feedback-loop` | Adjust vision/roadmap |
| Coordinator | `/speckit.aide.create-queue` | Generate next work batch |
| Coordinator | `/speckit.aide.create-item` | Create item specs |
| Coordinator | `/speckit.maqa-github-projects.populate` | Sync board |
| Coordinator | `/speckit.worktree.create` | Spawn engineer worktrees |
| Coordinator | `/speckit.worktree.list` | Check active worktrees |
| Coordinator | `/speckit.analyze` | Cross-artifact consistency audit (after each batch) |
| Coordinator | `/speckit.memorylint.run` | AGENTS.md vs constitution drift (after each batch) |
| Engineer | `/speckit.maqa.feature` | Run the engineer loop |
| Engineer | `/speckit.verify-tasks.run` | No phantom completions |
| Engineer | `/speckit.verify` | Spec acceptance criteria |
| Engineer | `/speckit.worktree.clean` | Remove merged worktree |
| Engineer | `/speckit.git.commit` | Auto-commit |
| QA | `/speckit.maqa.qa` | Run the QA loop |
| All | `/speckit.maqa-ci.check` | Check CI status on branch |

## Escalation Protocol

Max retries before escalating: **2**

An agent that cannot proceed MUST:
1. Label the GitHub Issue `blocked` or `needs-human`
2. Write a comment with: what is blocking, which file/line, what decision is needed
3. Set `.maqa/state.json` item state to `blocked`
4. Stop. No workarounds.

Human reads it, resolves it, removes the label. Coordinator resumes.

Human-in-the-loop gates (always escalate):
- New Go module dependency in go.mod
- CRD API field change not in the spec
- Go interface signature change
- Spec contradicts user docs or examples
- Test failure that cannot be explained

## Anti-Patterns

| Pattern | How it's caught |
|---|---|
| Task [x] without implementation | `/speckit.verify-tasks.run` + QA checklist |
| Missing manual validation evidence in PR | QA checklist item #3 |
| Mutating Deployments/Services directly | `/speckit.verify` |
| kro import in go.mod | CI + QA |
| Missing Apache 2.0 header | CI `check-headers` job + QA |
| util.go / helpers.go / common.go | CI `check-banned-filenames` job + QA |
| No idempotency test on reconciler | QA checklist |
| Feature not in user docs | `/speckit.verify` |
| go.mod not tidy | CI `verify-go-mod-tidy` step |
| No smoke test after merge | Engineer step 8 (go build ./...) |

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
- `docs/aide/team.yml`
