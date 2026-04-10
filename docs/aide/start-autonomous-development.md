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
| 2 | `kardinal-promoter/` | Engineer 1 | `/speckit.maqa.feature` |
| 3 | `kardinal-promoter/` | Engineer 2 | `/speckit.maqa.feature` |
| 4 | `kardinal-promoter/` | Engineer 3 | `/speckit.maqa.feature` |
| 5 | `kardinal-promoter/` | QA | `/speckit.maqa.qa` |
| 6 | `kardinal-promoter/` | Scrum Master | `/speckit.maqa.scrummaster` |
| 7 | `kardinal-promoter/` | Product Manager | `/speckit.maqa.pm` |

All sessions start in the main repo directory. The coordinator creates worktrees for engineers.
Engineers `cd` into their worktree when picking up work.
Scrum Master and PM work in the main repo — no worktrees needed.

---

## Setup (each session, once)

```bash
export GH_TOKEN=<token-with-repo-and-project-scopes>

# Session 1:  export AGENT_ID="COORDINATOR"
# Session 2:  export AGENT_ID="ENGINEER-1"
# Session 3:  export AGENT_ID="ENGINEER-2"
# Session 4:  export AGENT_ID="ENGINEER-3"
# Session 5:  export AGENT_ID="QA"
# Session 6:  export AGENT_ID="SCRUM-MASTER"
# Session 7:  export AGENT_ID="PM"

git pull origin main
```

All sessions share the same GitHub account. The `AGENT_ID` badge prefixes every
comment and PR review so you can tell who said what in your notification feed.

---

## Session Lifecycle

| Session | Runs until |
|---|---|
| Coordinator | All journeys ✅ Complete |
| Engineers | No more work assigned + queue exhausted |
| QA | No open PRs + coordinator posted final [PROJECT COMPLETE] |
| Scrum Master | Coordinator posted [PROJECT COMPLETE] |
| Product Manager | Coordinator posted [PROJECT COMPLETE] |

All sessions loop or react continuously:
- Engineers do not stop between items
- QA polls for new PRs continuously
- Scrum Master and PM run once per batch (triggered by coordinator [BATCH COMPLETE])

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

## What Scrum Master Does (after each batch)

Triggered by coordinator posting `[BATCH COMPLETE]` to Issue #1.

1. Reads `.maqa/state.json` and Issue #1 history — computes flow metrics
2. Inspects SDLC files: `sdlc.md`, `team.yml`, templates, `AGENTS.md` process sections
3. Checks: do agents actually follow the documented process?
4. Opens `sdlc-improvement` Issues for systemic problems
5. Applies minor fixes (< 10 lines) as direct PRs
6. Posts `[SDLC REVIEW]` to Issue #1

Does NOT touch: vision, roadmap, journeys, user docs, code.

---

## What Product Manager Does (after each batch)

Triggered by coordinator posting `[BATCH COMPLETE]` to Issue #1.

1. Checks vision alignment: do shipped features match the vision?
2. Checks journey coverage: are journeys still the right acceptance criteria?
3. Checks user doc freshness: do docs describe the current product?
4. Fixes stale user docs directly via PR
5. Opens `product-gap` Issues for competitor features we're missing
6. Opens `product-proposal` Issues for improvements (human prioritizes)
7. Every 3 batches: researches Kargo/GitOps Promoter/Flux releases for gaps
8. Posts `[PRODUCT REVIEW]` to Issue #1

Does NOT touch: sdlc.md, team.yml, templates, code.

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
