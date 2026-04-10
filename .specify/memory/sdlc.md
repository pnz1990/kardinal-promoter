# Reusable Autonomous SDLC

This document defines the team structure, agent roles, and development process
for fully autonomous AI-driven software development. It is project-agnostic.

To reuse on a new project: copy the files listed in the "Reuse" section at the
bottom, then customize AGENTS.md and docs/aide/definition-of-done.md for your
project.

---

## Team Structure

Five concurrent sessions, one role each:

| Session | Role | Command | Runs until |
|---|---|---|---|
| 1 | Coordinator | `/speckit.maqa.coordinator` | All journeys complete |
| 2 | Engineer 1 | `/speckit.maqa.feature` | No more work + queue empty |
| 3 | Engineer 2 | `/speckit.maqa.feature` | No more work + queue empty |
| 4 | Engineer 3 | `/speckit.maqa.feature` | No more work + queue empty |
| 5 | QA | `/speckit.maqa.qa` | No open PRs + final batch complete |

All sessions start in the main repository directory. The coordinator creates
worktrees for engineers. Engineers `cd` into their worktree after assignment.

---

## Session Startup (every session)

```bash
cd /path/to/project
export GH_TOKEN=<token-with-repo-and-project-scopes>
export AGENT_ID="<ROLE>"    # COORDINATOR | ENGINEER-1 | ENGINEER-2 | ENGINEER-3 | QA
git pull origin main
```

Every GitHub comment, issue update, and PR review MUST be prefixed with the
agent's identity badge (defined in AGENTS.md) so the human can tell who said
what in their notification feed.

---

## Reading Order (every agent, every session)

Read in this exact order before doing anything:

1. `docs/aide/vision.md` — what we are building and why
2. `docs/aide/roadmap.md` — staged delivery plan
3. `docs/aide/progress.md` — what is done, in progress, planned
4. `docs/aide/definition-of-done.md` — the journeys that define completion
5. `.specify/memory/constitution.md` — principles that override everything
6. `.specify/memory/sdlc.md` — this file (the process)
7. `docs/aide/team.yml` — your role's rules and the lifecycle
8. `AGENTS.md` — project-specific context (tech stack, commands, anti-patterns)
9. Your assigned item: `docs/aide/items/<NNN>.md`

---

## Coordinator Loop

Runs continuously until all journeys in `definition-of-done.md` are passing.

```
LOOP:

1. Read progress.md + roadmap.md → determine next stage
2. If queue is empty:
   - /speckit.aide.create-queue   → docs/aide/queue/queue-NNN.md
   - /speckit.aide.create-item    → docs/aide/items/NNN-*.md per item
   - /speckit.maqa-github-projects.populate
3. Validate: all Depends-on items are state=done before assigning
4. Assign items to engineer slots (max 3 concurrent):
   - /speckit.worktree.create
   - Write to .maqa/state.json: slot, item_id, state, worktree_path
   - Move GitHub Projects card: Todo → In Progress
   - Comment on item Issue: "[BADGE] Assigned to <SLOT>. Worktree: <path>"
5. Monitor .maqa/state.json every 2 min:
   - in_review → move card to In Review
   - done → move card to Done, free slot, assign next item
   - blocked → post [NEEDS HUMAN] to report issue, continue others
6. When all queue items are done or blocked:
   BATCH AUDIT (before generating next queue):
   - /speckit.analyze               spec ↔ tasks ↔ implementation consistency
   - /speckit.memorylint.run        AGENTS.md vs constitution drift
   - <project build command>        full project still compiles
   - <project test command>         regression suite on main
   - <project vuln scan>            security scan
   DOC FRESHNESS: docs/ matches implementation for user-facing features
   SPEC TRACEABILITY: every FR-NNN has a test
   JOURNEY STATUS: which journeys now pass end-to-end? Update definition-of-done.md table
   DYNAMIC EXPANSION: add new specs/journeys if gaps discovered (see below)
   If audit passes: update progress.md, post [BATCH COMPLETE] to report issue, go to 1
   If audit fails: post [BATCH QUALITY GATE FAILED], label needs-human, stop
7. When ALL journeys are ✅: post [PROJECT COMPLETE], exit

DYNAMIC EXPANSION (coordinator does this, no human needed):
  New spec triggers: edge cases not covered, unspecified behavior, design doc gap
  How: create .specify/specs/NNN-name/spec.md + tasks.md, update constitution + progress.md
  New journey triggers: new user-facing capability, new user workflow
  How: add journey to definition-of-done.md, add test stub, add make target
  Report both via [NEW SPEC] / [NEW JOURNEY] on report issue

RULES:
- Never implement features. Never commit. Never push. Never merge.
- Engineers merge their own PRs.
- Never assign if Depends-on items are not done.
- Never generate next queue if audit failed.
- Never skip the batch audit.
```

---

## Engineer Loop

Owns each feature end-to-end from assignment through merged PR.

```
LOOP (one feature per iteration):

1. PICK UP
   Poll .maqa/state.json every 2 min for item assigned to my slot (AGENT_ID)
   Read: worktree_path from state.json
   cd into worktree: cd <worktree_path>
   Wait 1 min and re-poll if worktree doesn't exist yet
   Read: docs/aide/items/<item>.md → .specify/specs/<feature>/spec.md
        → tasks.md → docs/design/<feature>.md → examples/ → docs/

2. IMPLEMENT (TDD — strict order)
   Write failing test FIRST, before any implementation
   Implement until <test command> passes
   <lint/vet command> must show zero findings
   Tick each task in tasks.md ONLY after its code exists

3. SELF-VALIDATE (mandatory, no exceptions)
   /speckit.verify-tasks.run — zero phantom completions
   /speckit.verify — all acceptance criteria pass
   Run the journey steps this feature contributes to (from definition-of-done.md)
   Capture output — it goes in the PR body as journey validation evidence
   If journey step does not produce documented result: fix, re-test, re-validate

4. PUSH PR
   Open PR using docs/aide/pr-template.md
   Title: "feat(<scope>): <description>"  (Conventional Commits)
   Body MUST include: item ID, spec ref, acceptance criteria checked,
                      test output, verify-tasks output, journey validation output
   Set .maqa/state.json item state = in_review

5. MONITOR CI
   Poll CI every 3 min: gh pr checks <pr-number>
   If red: read failure, fix, push new commit
   Do NOT proceed until ALL checks green

6. RESPOND TO QA
   Poll PR for reviews every 5 min: gh pr view <pr-number> --json reviews
   If QA requests changes: fix all issues (file:line references), push, go to 5
   If QA approves AND CI green: proceed to 7

7. MERGE
   gh pr merge <pr-number> --squash --delete-branch
   /speckit.worktree.clean
   Set .maqa/state.json item state = done
   Post on item Issue: "[BADGE] Merged in PR #N. Feature complete."

8. SMOKE TEST ON MAIN
   git checkout main && git pull
   <project build command>
   If fails: open hotfix issue, label needs-human, stop

9. LOOP → step 1

ESCALATION (max 2 retries before escalating):
- Dependency not done → set state=backlog, label blocked, notify coordinator
- Spec ambiguity → label needs-human, write exact question, stop
- Unexplained test failure → label needs-human, paste full output, stop
- New dependency addition → label needs-human (human gate), stop
```

---

## QA Loop

Continuous PR watcher.

```
LOOP:

1. Poll every 2 min: gh pr list --label <project-pr-label> --state open
2. For each PR with new commits since last review:
   Read full diff
   Read: docs/aide/items/<item>.md, .specify/specs/<feature>/spec.md
   Run: /speckit.verify on the branch
   CHECKLIST (all must pass):
   □ Every Given/When/Then acceptance scenario from spec.md implemented
   □ Every FR-NNN has real code (not stub or no-op)
   □ PR body includes journey validation output (manual test evidence)
   □ PR body includes /speckit.verify-tasks.run output (zero phantom completions)
   □ <project lint/vet> passes (check CI)
   □ <project copyright header> on all new source files
   □ No banned filenames (project-specific list in AGENTS.md)
   □ <project error handling pattern> used correctly
   □ Every new reconciler/handler has idempotency test
   □ <project anti-patterns from AGENTS.md> absent
   □ docs/ consistent with implementation (if user-facing)
   □ examples/ YAML applies cleanly
   □ Journey the feature contributes to is one step closer to passing
3. POST REVIEW
   PASS: gh pr review <N> --approve --body "[BADGE] LGTM. All criteria satisfied."
   FAIL: gh pr review <N> --request-changes --body "[BADGE] ## Changes Required\n<file:line issues>"
4. After requesting changes: poll PR every 5 min for new commits
   Re-review FULL diff on every new commit (not just delta)
5. Escalate to report issue after same issue appears 3+ times
6. LOOP → step 1
STOP: No open PRs AND coordinator posted [PROJECT COMPLETE]
```

---

## Escalation Protocol

Max retries before escalating: **2**

Agent cannot proceed → MUST:
1. Label the GitHub Issue `blocked` or `needs-human`
2. Comment with: agent badge, what is blocking, file/line, exact decision needed
3. Set `.maqa/state.json` item state = `blocked`
4. Stop. No workarounds.

Human reads it, resolves it, removes the label. Coordinator resumes within 2 min.

Human-in-the-loop gates (always escalate, never attempt alone):
- New external dependency not in the existing dependency list
- API contract / interface signature change not in the spec
- Spec contradicts user docs or examples
- Test failure not explained by the implementation
- Security finding

---

## Reporting

All reports go to a single standing GitHub Issue (the "report issue").
The issue number and URL are configured in `docs/aide/team.yml`.
Subscribe to this issue to receive all team updates.

Report types posted by the coordinator:
- `[BATCH COMPLETE]` — queue batch finished, what shipped, what's blocked, journey status
- `[NEEDS HUMAN]` — agent blocked, human decision required
- `[QA FINDING]` — QA escalated a finding (severity + file:line)
- `[BATCH QUALITY GATE FAILED]` — audit failed, next queue blocked
- `[NEW SPEC]` — coordinator added a new spec during dynamic expansion
- `[NEW JOURNEY]` — coordinator added a new journey
- `[PROJECT COMPLETE]` — all journeys passing

Every comment prefixed with agent badge: `[🎯 COORDINATOR]`, `[🔨 ENGINEER-N]`, `[🔍 QA]`.

---

## State File

`.maqa/state.json` — the team's shared state. Written atomically (tmp file then rename).

```json
{
  "version": "1.0",
  "current_queue": "queue-001",
  "engineer_slots": {
    "ENGINEER-1": null,
    "ENGINEER-2": null,
    "ENGINEER-3": null
  },
  "features": {
    "<item-id>": {
      "state": "in_progress | in_review | done | blocked",
      "assigned_to": "ENGINEER-1",
      "worktree_path": "../<project>.<feature-branch>",
      "pr_number": null,
      "pr_merged": false
    }
  }
}
```

Multiple agents poll this file. Each agent only modifies its own assigned item.

---

## Reuse: How to Use This SDLC on a New Project

### Files to copy (the generic SDLC kit)

```
.specify/
  memory/
    constitution.md     → keep all principles except I-III (project-specific tech)
    sdlc.md             → this file, copy as-is
  templates/
    overrides/
      spec-template.md  → copy as-is
      tasks-template.md → copy as-is
  extensions.yml        → copy, re-run specify extension add for each
  extension-catalogs.yml

docs/aide/
  team.yml              → copy, update: report issue URL, board URL, CI commands

maqa-config.yml         → copy, update: test_command, board
maqa-ci/ci-config.yml   → copy, update: owner, repo, provider
maqa-github-projects/
  github-projects-config.yml  → run /speckit.maqa-github-projects.setup

.maqa/state.json        → copy as-is (will be reset at first coordinator run)
.specifyignore          → copy, update for project file patterns
Makefile                → copy structure, update build/test/lint commands
.github/workflows/ci.yml     → copy structure, update Go-specific steps for your stack
.github/workflows/release.yml → copy, update build + package commands
.github/workflows/e2e.yml    → copy, update test runner
.github/CODEOWNERS      → copy, update username
.github/PULL_REQUEST_TEMPLATE.md → copy as-is
.github/ISSUE_TEMPLATE/  → copy as-is
.github/dependabot.yml   → copy, update package-ecosystem for your stack
```

### Files to create fresh (project-specific content)

```
docs/aide/vision.md              → your product vision
docs/aide/roadmap.md             → your staged delivery plan
docs/aide/progress.md            → start with all stages Planned
docs/aide/definition-of-done.md → your journeys (user-facing acceptance tests)
AGENTS.md                        → your tech stack, architecture, commands,
                                   anti-patterns, identity badges
.specify/specs/                  → your feature specs (generated by coordinator)
.specify/memory/constitution.md  → keep generic principles, remove/replace tech-specific ones
```

### What the human provides to start

1. Write `docs/aide/vision.md` — what the product is and why
2. Write `docs/aide/roadmap.md` — how to build it in stages
3. Write `docs/aide/definition-of-done.md` — the 3-5 journeys that prove it works
4. Write `AGENTS.md` — tech stack, package structure, language standards, PR label
5. Configure the GitHub repo, Projects board, report issue, branch protection
6. Run `specify init` + install extensions + run `/speckit.maqa-github-projects.setup`
7. Open 5 sessions, set `AGENT_ID`, run role commands
8. Watch the board. Unblock `needs-human` labels. That's it.
