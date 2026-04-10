# How to Start Autonomous Development

This document is the single reference for starting the autonomous team. Read it, open the sessions, and step away.

---

## Prerequisites (one-time, already done)

- [x] GitHub repo: https://github.com/pnz1990/kardinal-promoter
- [x] GitHub Projects board: https://github.com/users/pnz1990/projects/1
- [x] Report issue: https://github.com/pnz1990/kardinal-promoter/issues/1 (subscribe to this)
- [x] spec-kit v0.6.0 + all extensions installed
- [x] `maqa-github-projects/github-projects-config.yml` configured
- [x] `maqa-ci/ci-config.yml` configured for GitHub Actions
- [x] `maqa-config.yml` configured (`go test ./... -race`, TDD, max_parallel: 3)
- [x] Vision, roadmap, progress, specs, and design docs in place

---

## The Team

Five OpenCode sessions, five roles. Each session is one agent.

| Session | Directory | Command to run | Role |
|---|---|---|---|
| 1 | `kardinal-promoter/` (main) | `/speckit.maqa.coordinator` | Coordinator |
| 2 | `kardinal-promoter.feat-1/` | `/speckit.maqa.feature` | Engineer 1 |
| 3 | `kardinal-promoter.feat-2/` | `/speckit.maqa.feature` | Engineer 2 |
| 4 | `kardinal-promoter.feat-3/` | `/speckit.maqa.feature` | Engineer 3 |
| 5 | `kardinal-promoter/` (main) | `/speckit.maqa.qa` | QA |

They do not talk to each other directly. They communicate through files and GitHub:
- Coordinator writes assignments to `.maqa/state.json` and GitHub Issues
- Engineers read their assignment from `.maqa/state.json`, work in their worktree, push a PR
- QA watches for open PRs labeled `kardinal`, reviews against spec + user docs
- Coordinator watches for PR merges and QA decisions, updates the board

---

## Before Starting

In a terminal:

```bash
cd /Users/rrroizma/Projects/kardinal-promoter
export GH_TOKEN=<your-token-with-repo-and-project-scopes>
git pull origin main
```

---

## Step 1 — Start the Coordinator (Session 1)

Open an OpenCode session in `kardinal-promoter/` and run:

```
/speckit.maqa.coordinator
```

The coordinator will:
1. Read `docs/aide/roadmap.md` and `docs/aide/progress.md`
2. Generate work items (`docs/aide/queue/`, `docs/aide/items/`)
3. Populate the GitHub Projects board (items appear in Todo)
4. Write assignments to `.maqa/state.json`
5. Wait for engineers and QA to pick up work

---

## Step 2 — Start the Engineers (Sessions 2, 3, 4)

For each engineer, create a worktree and open an OpenCode session:

```bash
# Engineer 1
/speckit.worktree.create   # coordinator runs this, or you run it manually
cd ../kardinal-promoter.feat-1
export GH_TOKEN=<token>
# open OpenCode session here
```

In each engineer session, run:

```
/speckit.maqa.feature
```

The engineer will:
1. Read its assignment from `.maqa/state.json`
2. Read the item file from `docs/aide/items/<item>.md`
3. Read the spec from `.specify/specs/<feature>/spec.md`
4. Write tests first, implement, run `go test ./... -race`
5. Run `/speckit.verify-tasks.run` and `/speckit.verify`
6. Push branch and open a PR using `docs/aide/pr-template.md`
7. Report back to coordinator via `.maqa/state.json`

---

## Step 3 — Start QA (Session 5)

Open an OpenCode session in the main `kardinal-promoter/` directory and run:

```
/speckit.maqa.qa
```

QA will:
1. Watch for open PRs labeled `kardinal` on the repo
2. For each PR: read the diff, check against the spec and user docs
3. Run `/speckit.verify` against the PR branch
4. Post a GitHub PR review — approve or request changes with `file:line` references
5. If a security issue or architecture deviation is found: post `[QA FINDING]` to Issue #1

---

## How PRs Get Merged

When QA approves a PR and CI is green, the coordinator merges it:
```bash
gh pr merge <number> --squash --delete-branch
```

The coordinator then:
- Updates `.maqa/state.json` → `done`
- Moves the GitHub Projects card to Done
- Assigns the next item to the freed engineer

---

## Your Job While It Runs

**Check the board**: https://github.com/users/pnz1990/projects/1
Cards move: `Todo → In Progress → In Review → Done`

**Read reports**: https://github.com/pnz1990/kardinal-promoter/issues/1
The coordinator posts:
- `[BATCH COMPLETE]` — batch finished, summary of what shipped
- `[NEEDS HUMAN]` — agent blocked, needs a decision
- `[QA FINDING]` — QA found something worth attention

**Unblock escalations**: GitHub Issues labeled `needs-human`.
Read the comment. Make the decision. Remove the label. The coordinator resumes.

**That's it.** You do not create queue files, item files, specs, or code.

---

## Session Lifecycle

| Session | Runs until |
|---|---|
| Coordinator | All 20 roadmap stages complete, or every remaining item is `needs-human` |
| Each Engineer | Coordinator marks their slot idle, or their item hits `needs-human` after 2 retries |
| QA | No open PRs AND coordinator has posted final `[BATCH COMPLETE]` |

**They do not stop between items.** Engineers immediately pick up the next assignment. QA continuously polls for new PRs. The coordinator continuously generates new queues. The entire project builds itself from Stage 0 to Stage 19 without you restarting anything.

---

**Coordinator:** Restart in `kardinal-promoter/`, run `/speckit.maqa.coordinator`. It reads `.maqa/state.json` and resumes.

**Engineer:** Restart in the worktree directory, run `/speckit.maqa.feature`. It reads its current assignment and resumes from the current step index (stored in PromotionStep status — no work lost).

**QA:** Restart in `kardinal-promoter/`, run `/speckit.maqa.qa`. It re-scans open PRs.

---

## If You Want to Adjust Direction

Edit `docs/aide/vision.md` or `docs/aide/roadmap.md`, then in a session run:

```
/speckit.aide.feedback-loop
```

Already-in-progress items are not affected. The next queue will reflect the changes.

---

## Labels Reference

| Label | Meaning | Your action |
|---|---|---|
| `needs-human` | Agent blocked, needs a decision | Read the issue, answer, remove the label |
| `blocked` | Item waiting on a dependency | No action — coordinator handles it |
| `report` | Progress report from coordinator | Read, no action required |

---

## Key Files

| File | Owner | Purpose |
|---|---|---|
| `docs/aide/vision.md` | Human | Product vision |
| `docs/aide/roadmap.md` | Human | 20-stage delivery plan |
| `docs/aide/progress.md` | Coordinator | Stage completion status |
| `docs/aide/team.yml` | Human | Agent roles, rules, reporting |
| `docs/aide/queue/` | Coordinator | Work item queues (generated) |
| `docs/aide/items/` | Coordinator | Detailed item specs (generated) |
| `.maqa/state.json` | Coordinator | Feature lifecycle state |
| `maqa-config.yml` | Human | Test command, parallelism, board |
| `AGENTS.md` | Human | Full agent context (all agents read this) |
| `docs/aide/start-autonomous-development.md` | Human | This file |
