# How to Start Autonomous Development

Read this. Open 5 terminal sessions. Step away.

---

## Prerequisites (all done)

- [x] GitHub repo + branch protection on main (requires 1 QA approval + CI)
- [x] GitHub Projects board: https://github.com/users/pnz1990/projects/1
- [x] Report issue #1 (subscribe): https://github.com/pnz1990/kardinal-promoter/issues/1
- [x] GitHub Actions CI workflow (`.github/workflows/ci.yml`)
- [x] spec-kit v0.6.0 + 8 active extensions
- [x] maqa-github-projects configured (project ID + status field IDs)
- [x] maqa-ci configured (GitHub Actions, pnz1990/kardinal-promoter)
- [x] maqa-config.yml (go test ./... -race, TDD mode, max 3 parallel)
- [x] Vision, roadmap, progress, specs, and design docs in place

---

## The Team

| Session | Directory | Role | Command |
|---|---|---|---|
| 1 | `kardinal-promoter/` | Coordinator | `/speckit.maqa.coordinator` |
| 2 | `kardinal-promoter.feat-1/` | Engineer 1 | `/speckit.maqa.feature` |
| 3 | `kardinal-promoter.feat-2/` | Engineer 2 | `/speckit.maqa.feature` |
| 4 | `kardinal-promoter.feat-3/` | Engineer 3 | `/speckit.maqa.feature` |
| 5 | `kardinal-promoter/` | QA | `/speckit.maqa.qa` |

Sessions 2-4 start as empty directories. The coordinator creates the worktrees.

---

## Setup (each session, once)

```bash
export GH_TOKEN=<token-with-repo-and-project-scopes>
git pull origin main
```

---

## Session Lifecycle

| Session | Runs until |
|---|---|
| Coordinator | All 20 roadmap stages ✅ Complete |
| Engineers | No more work assigned + queue exhausted |
| QA | No open PRs + coordinator posted final [BATCH COMPLETE] |

All sessions loop continuously. Engineers do not stop between items.
Engineers own each feature end-to-end: assignment → merge → smoke test → next item.

---

## What the Coordinator Does (automatically)

1. Reads `docs/aide/progress.md` to find the next stage
2. Generates `docs/aide/queue/queue-NNN.md` (work items)
3. Generates `docs/aide/items/NNN-*.md` (detailed specs per item)
4. Populates the GitHub Projects board
5. Creates worktrees for engineers
6. Assigns items respecting dependency order (max 3 parallel)
7. Tracks state in `.maqa/state.json`
8. Posts batch reports to Issue #1
9. Repeats until all 20 stages complete

---

## What Engineers Do (automatically, end-to-end)

1. Pick up assigned item
2. Write failing tests first (TDD)
3. Implement until `go test ./... -race` passes
4. Manually validate against `examples/` — kubectl apply
5. Push PR with evidence (test output + kubectl output)
6. Monitor CI every 3 min — fix red, re-push
7. Poll for QA review every 5 min — fix comments, re-push
8. Merge PR after QA approves + CI green
9. Smoke test: `go build ./...` on main
10. Pick up next item

---

## What QA Does (automatically, continuously)

1. Polls open PRs labeled `kardinal` every 2 min
2. Reviews full diff against spec + user docs + examples checklist
3. Posts `request-changes` with file:line references, or `approve`
4. Re-reviews full diff after every engineer fix commit
5. Escalates to Issue #1 after 3 failed fix attempts on same issue

---

## Your Job

**Board**: https://github.com/users/pnz1990/projects/1 — cards move Todo → In Progress → In Review → Done

**Reports**: https://github.com/pnz1990/kardinal-promoter/issues/1 — subscribe to this

**Act on `needs-human` labels**: Read the issue comment. Make the decision. Remove the label. The coordinator resumes within 2 minutes.

**Adjust direction**: Edit `docs/aide/vision.md` or `docs/aide/roadmap.md`, then run `/speckit.aide.feedback-loop` in a new session.

---

## If a Session Disconnects

Restart the session in the same directory and re-run the role command. All agents read `.maqa/state.json` to resume. No work is lost.

---

## Labels

| Label | Meaning | Your action |
|---|---|---|
| `needs-human` | Agent blocked, decision needed | Answer in the issue comment, remove label |
| `blocked` | Waiting on a dependency | No action — coordinator handles |
| `report` | Coordinator progress update | Read, no action required |

---

## Key Files

| File | Owner | Purpose |
|---|---|---|
| `docs/aide/vision.md` | Human | Product vision |
| `docs/aide/roadmap.md` | Human | 20-stage delivery plan |
| `docs/aide/progress.md` | Coordinator | Stage completion status |
| `docs/aide/team.yml` | Human | Roles, rules, lifecycle |
| `docs/aide/queue/` | Coordinator | Work item queues |
| `docs/aide/items/` | Coordinator | Item specs |
| `.maqa/state.json` | All agents | Feature lifecycle state |
| `maqa-config.yml` | Human | Test command, parallelism |
| `.github/workflows/ci.yml` | Human | CI (headers, vet, test, build) |
| `AGENTS.md` | Human | Full agent context |
