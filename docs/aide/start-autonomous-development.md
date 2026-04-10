# How to Start Autonomous Development

This document is the single reference for starting the autonomous team. Read it, run the one command, and step away.

---

## Prerequisites (one-time, already done)

- [x] GitHub repo: https://github.com/pnz1990/kardinal-promoter
- [x] GitHub Projects board: https://github.com/users/pnz1990/projects/1
- [x] Report issue: https://github.com/pnz1990/kardinal-promoter/issues/1 (subscribe to this)
- [x] spec-kit v0.6.0 + all extensions installed
- [x] `maqa-github-projects/github-projects-config.yml` configured with project ID and status field IDs
- [x] `maqa-ci/ci-config.yml` configured for GitHub Actions
- [x] `maqa-config.yml` configured (`go test ./... -race`, TDD, max_parallel: 3)
- [x] Vision, roadmap, progress, specs, and design docs in place

---

## Before Starting Each Session

```bash
cd /Users/rrroizma/Projects/kardinal-promoter
export GH_TOKEN=<your-token-with-repo-and-project-scopes>
git pull origin main
```

---

## Start the Autonomous Loop

Open a new OpenCode session in the kardinal-promoter directory and run:

```
/speckit.maqa.coordinator
```

That is the only command you need to run. The coordinator will:

1. Read `docs/aide/roadmap.md` and `docs/aide/progress.md`
2. Generate the next queue of work items (`docs/aide/queue/queue-NNN.md`)
3. Create detailed item specs (`docs/aide/items/`)
4. Populate the GitHub Projects board with items in Todo
5. Spawn up to 3 feature agents in parallel, each in an isolated git worktree
6. Gate on CI (GitHub Actions must be green)
7. Spawn the QA agent to review each PR
8. Merge approved PRs, update the board, update progress
9. Loop — generate the next queue and repeat until the roadmap is complete

---

## Your Job While It Runs

**Check the board**: https://github.com/users/pnz1990/projects/1
Cards move: `Todo → In Progress → In Review → Done`

**Subscribe to reports**: https://github.com/pnz1990/kardinal-promoter/issues/1
The coordinator posts a comment when:
- A batch completes (`[BATCH COMPLETE]`)
- An agent is blocked (`[NEEDS HUMAN]`)
- QA finds something worth attention (`[QA FINDING]`)

**Unblock escalations**: Watch for GitHub Issues labeled `needs-human`. Read the comment, make the decision, remove the label. The coordinator resumes automatically.

**That's it.** You do not create queue files, item files, specs, or code.

---

## If the Session Disconnects

Restart from the same directory:

```
/speckit.maqa.coordinator
```

The coordinator reads `.maqa/state.json` to resume from where it left off. No work is lost.

---

## If You Want to Adjust Direction

Edit `docs/aide/vision.md` or `docs/aide/roadmap.md`, then run:

```
/speckit.aide.feedback-loop
```

This reflects the changes into the next queue generation. Already-in-progress items are not affected.

---

## Labels Reference

| Label | Meaning | Your action |
|---|---|---|
| `needs-human` | Agent blocked, needs a decision | Read the issue, answer, remove the label |
| `blocked` | Item waiting on a dependency | No action needed, coordinator handles it |
| `report` | Progress report from coordinator | Read, no action required |

---

## Key Files

| File | Purpose |
|---|---|
| `docs/aide/vision.md` | Product vision (human-owned) |
| `docs/aide/roadmap.md` | 20-stage delivery plan (human-owned) |
| `docs/aide/progress.md` | Stage completion status (coordinator updates) |
| `docs/aide/team.yml` | Agent roles, rules, reporting config |
| `docs/aide/queue/` | Work item queues (coordinator generates) |
| `docs/aide/items/` | Detailed item specs (coordinator generates) |
| `.maqa/state.json` | Coordinator state (feature lifecycle tracking) |
| `maqa-config.yml` | Team config (test command, parallelism, board) |
| `AGENTS.md` | Full agent context (all agents read this) |
