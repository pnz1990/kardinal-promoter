# Reusable Autonomous SDLC

This document defines the team structure, agent roles, and development process
for fully autonomous AI-driven software development. It is project-agnostic.

To reuse on a new project: copy the files listed in the "Reuse" section at the
bottom, then customize AGENTS.md and docs/aide/definition-of-done.md for your
project.

---

## Team Structure

Seven concurrent sessions, one role each:

| Session | Role | Command | Runs until |
|---|---|---|---|
| 1 | Coordinator | `/speckit.maqa.coordinator` | All journeys complete |
| 2 | Engineer 1 | `/speckit.maqa.feature` | No more work + queue empty |
| 3 | Engineer 2 | `/speckit.maqa.feature` | No more work + queue empty |
| 4 | Engineer 3 | `/speckit.maqa.feature` | No more work + queue empty |
| 5 | QA | `/speckit.maqa.qa` | No open PRs + final batch complete |
| 6 | Scrum Master | `/speckit.maqa.scrummaster` | Project complete |
| 7 | Product Manager | `/speckit.maqa.pm` | Project complete |

All sessions start in the main repository directory. The coordinator creates
worktrees for engineers. Engineers `cd` into their worktree after assignment.

The Scrum Master and Product Manager do not have worktrees. They work in the
main repository and propose changes via GitHub Issues and PRs.

---

## Session Startup (every session)

```bash
cd /path/to/project
export GH_TOKEN=<token-with-repo-and-project-scopes>
export AGENT_ID="<ROLE>"    # COORDINATOR | ENGINEER-1 | ENGINEER-2 | ENGINEER-3 | QA | SCRUM-MASTER | PM
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
   If audit passes:
     - update progress.md
     - post [BATCH COMPLETE] to report issue
     - SPAWN SCRUM MASTER: notify Session 6 to run its review cycle
     - SPAWN PRODUCT MANAGER: notify Session 7 to run its review cycle
     - go to step 1
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
- Spawn Scrum Master and PM after every successful batch.
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

## Scrum Master Loop

Runs once per batch (triggered by coordinator after [BATCH COMPLETE]).
Owns and improves the SDLC layer. Does not touch product content.

```
TRIGGER: Coordinator posts [BATCH COMPLETE] to report issue.
         Scrum Master reads the report and runs one inspection cycle.

READS (SDLC layer — the only files this role touches):
  .specify/memory/sdlc.md          → the process itself
  .specify/memory/constitution.md  → principles
  docs/aide/team.yml               → roles, rules, lifecycle
  .specify/templates/overrides/    → spec and tasks templates
  AGENTS.md                        → process sections only (not product/architecture)
  .maqa/state.json                 → flow metrics (cycle times, retry counts)
  docs/aide/queue/                 → queue health (items blocked vs done)
  Issue #1 history                 → NEEDS HUMAN frequency, QA rejection rates

DOES NOT READ OR MODIFY (product layer):
  docs/aide/vision.md
  docs/aide/roadmap.md
  docs/aide/definition-of-done.md
  .specify/specs/                  (content — may update process sections in templates)
  docs/ user documentation
  examples/

INSPECTION CYCLE:

1. FLOW ANALYSIS — read .maqa/state.json and Issue #1 history
   Compute for this batch:
   - Average time per item (todo → done)
   - QA rejection rate (% of PRs that needed changes)
   - NEEDS HUMAN frequency (how many per batch)
   - Blocked item rate (how many went backlog → blocked)
   If any metric is deteriorating: identify root cause before proceeding

2. SDLC HEALTH CHECKS
   □ Does sdlc.md accurately reflect what the team actually does?
     (compare to Issue #1 reports — do agents follow the documented process?)
   □ Are engineer self-validation steps catching issues before QA?
     (if QA rejection rate > 30%: engineers are not self-validating enough)
   □ Are NEEDS HUMAN escalations for the right reasons?
     (if agents escalate things they should handle: update sdlc.md to clarify)
   □ Are spec templates producing specs that engineers can implement without questions?
     (if specs need frequent clarification: update spec-template.md)
   □ Are tasks templates producing tasks that map 1:1 to code?
     (if tasks are too coarse or too fine: update tasks-template.md)
   □ Is constitution.md still accurate? Any new principles needed?
   □ Is team.yml still accurate? Any rules that are never followed or always violated?
   □ Has /speckit.memorylint.run identified drift between AGENTS.md and constitution?

3. PROPOSE IMPROVEMENTS
   For each issue found: open a GitHub Issue labeled sdlc-improvement with:
   - Current behavior observed
   - Proposed change to sdlc.md / team.yml / template
   - Expected improvement in metric
   - Files to change
   
   If improvement is minor (< 10 lines): open PR directly with the change.
   Title: "process(<scope>): <description>"

4. APPLY APPROVED CHANGES
   If any sdlc-improvement Issues have been resolved by human (label removed):
   Apply the changes via PR.
   Title: "process(<scope>): <description>"
   Never force changes that the human has not acknowledged.

5. REPORT
   Post [SDLC REVIEW] to report issue with:
   - Batch flow metrics
   - Issues found
   - Improvements proposed or applied
   - Any SDLC-level needs-human items

RULES:
- Only touch: sdlc.md, constitution.md, team.yml, spec/tasks templates, AGENTS.md process sections
- Never touch: vision, roadmap, definition-of-done, user docs, specs, code
- Never block the coordinator or engineers
- Improvements are proposals first, changes second
- If unsure whether something is product or process: it is product → escalate to PM
```

---

## Product Manager Loop

Runs once per batch (triggered by coordinator after [BATCH COMPLETE]).
Owns and evolves the product layer. Does not touch SDLC process files.

```
TRIGGER: Coordinator posts [BATCH COMPLETE] to report issue.
         PM reads the report and runs one product review cycle.

READS (product layer — the only files this role touches):
  docs/aide/vision.md              → product intent
  docs/aide/roadmap.md             → staged delivery
  docs/aide/definition-of-done.md → journeys and acceptance criteria
  docs/aide/progress.md            → what has shipped
  .specify/specs/                  → feature specifications
  docs/ user documentation         → quickstart, concepts, CLI reference, etc.
  examples/                        → working examples as acceptance tests
  Issue #1 history                 → [BATCH COMPLETE] reports, QA findings

DOES NOT READ OR MODIFY (SDLC layer):
  .specify/memory/sdlc.md
  .specify/memory/constitution.md (except product principles I-III)
  docs/aide/team.yml
  .specify/templates/
  .maqa/

REVIEW CYCLE:

1. PRODUCT HEALTH ASSESSMENT
   Read the batch report: what features shipped? What journeys advanced?
   Read user documentation: does it still accurately describe the product?
   Read examples: do they represent realistic user workflows?
   Read specs: are there gaps between what's specced and what's in the vision?

2. VISION ALIGNMENT CHECK
   □ Do shipped features match the vision?
     (if a feature shipped that is not in vision: raise for human review)
   □ Does the roadmap still make sense given what's been learned?
     (if stages are in wrong order or missing: propose roadmap update)
   □ Are the journeys still the right acceptance criteria?
     (if a journey no longer represents a real user flow: propose update)
   □ Are there user flows described in docs/ that don't have a journey?
     (if yes: propose a new journey)

3. SPEC REVIEW
   For each completed spec (state=done in progress.md):
   □ Does the user doc for this feature exist and accurately describe it?
   □ Does the example for this feature exist and work?
   □ Are there edge cases in the spec that are missing from user docs?
   □ If a user followed docs/quickstart.md right now, would this feature work?

4. COMPETITIVE ANALYSIS (periodic — every 3 batches)
   Research: what have competitors shipped since last analysis?
   Sources: GitHub releases, changelogs, docs, community discussions
   For each competitor finding: is this a gap in our product?
   Open GitHub Issue labeled product-gap for gaps worth addressing.

5. BACKLOG PROPOSALS
   For each gap or improvement found:
   Open GitHub Issue labeled product-proposal with:
   - User story (who benefits, what they can do, why it matters)
   - Which journey it improves or which new journey it enables
   - Rough scope (small/medium/large)
   - Files to create/modify
   Do NOT create specs directly — proposals go to the human for prioritization,
   then to coordinator to generate specs.

6. USER DOC FRESHNESS
   For each user doc page (docs/*.md):
   □ Does it describe what the current code actually does?
   □ Are all code examples current?
   □ Are there undocumented behaviors that users would encounter?
   If stale: open PR directly for doc corrections.
   Title: "docs(<scope>): <description>"

7. REPORT
   Post [PRODUCT REVIEW] to report issue with:
   - Vision alignment status
   - Journey coverage (which journeys are ✅ vs ❌)
   - Spec gaps found
   - User doc issues found or fixed
   - Competitive findings (if analysis ran)
   - Product proposals opened

RULES:
- Only touch: vision, roadmap, definition-of-done, progress, specs, user docs, examples
- Never touch: sdlc.md, constitution.md, team.yml, templates, code
- Never implement features — proposals only
- User doc fixes can be PRs; everything else is Issues for human prioritization
- Competitive analysis is research only — never blindly copy competitor features
- If unsure whether something is product or process: it is process → escalate to SM
```

---

## Escalation Protocol

Max retries before escalating: **2**

Agent cannot proceed → MUST:
1. Label the GitHub Issue `blocked` or `needs-human`
2. Comment with: agent badge, what is blocking, file/line, exact decision needed
3. Set `.maqa/state.json` item state = `blocked` (for engineers)
4. Stop. No workarounds.

Human reads it, resolves it, removes the label. Coordinator resumes within 2 min.

Human-in-the-loop gates (always escalate, never attempt alone):
- New external dependency not in the existing dependency list
- API contract / interface signature change not in the spec
- Spec contradicts user docs or examples
- Test failure not explained by the implementation
- Security finding
- Product proposal that would significantly change the roadmap

---

## Reporting

All reports go to a single standing GitHub Issue (the "report issue").
Subscribe to this issue to receive all team updates.

Report types:
- `[BATCH COMPLETE]` — coordinator: queue batch finished, journey status
- `[NEEDS HUMAN]` — any agent: blocked, human decision required
- `[QA FINDING]` — QA: escalated finding (severity + file:line)
- `[BATCH QUALITY GATE FAILED]` — coordinator: audit failed, queue blocked
- `[NEW SPEC]` — coordinator: dynamic spec expansion
- `[NEW JOURNEY]` — coordinator: dynamic journey expansion
- `[SDLC REVIEW]` — scrum master: process health and improvements
- `[PRODUCT REVIEW]` — PM: product health, gaps, proposals
- `[PROJECT COMPLETE]` — coordinator: all journeys passing

Badges: `[🎯 COORDINATOR]`, `[🔨 ENGINEER-N]`, `[🔍 QA]`, `[🔄 SCRUM-MASTER]`, `[📋 PM]`.

---

## State File

`.maqa/state.json` — the team's shared state. Written atomically.

```json
{
  "version": "1.0",
  "current_queue": "queue-001",
  "engineer_slots": {
    "ENGINEER-1": null,
    "ENGINEER-2": null,
    "ENGINEER-3": null
  },
  "last_sm_review": null,
  "last_pm_review": null,
  "batches_since_competitive_analysis": 0,
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

docs/aide/
  team.yml              → copy as-is (generic, no project-specific values)

maqa-config.yml         → copy, update: test_command, board
maqa-ci/ci-config.yml   → copy, update: owner, repo, provider
maqa-github-projects/
  github-projects-config.yml  → run /speckit.maqa-github-projects.setup

.maqa/state.json        → copy as-is (reset at first coordinator run)
.specifyignore          → copy, update for project file patterns
Makefile                → copy structure, update build/test/lint commands
.github/workflows/ci.yml     → copy, update stack-specific steps
.github/workflows/release.yml → copy, update build + package commands
.github/workflows/e2e.yml    → copy, update test runner
.github/CODEOWNERS      → copy, update username
.github/PULL_REQUEST_TEMPLATE.md → copy as-is
.github/ISSUE_TEMPLATE/  → copy as-is
.github/dependabot.yml   → copy, update package-ecosystem
```

### Files to create fresh (project-specific content)

```
docs/aide/vision.md              → your product vision
docs/aide/roadmap.md             → your staged delivery plan
docs/aide/progress.md            → start with all stages Planned
docs/aide/definition-of-done.md → your journeys (user-facing acceptance tests)
AGENTS.md                        → tech stack, architecture, commands,
                                   anti-patterns, identity badges, project config
.specify/memory/constitution.md  → keep generic principles, replace tech-specific
```

### What the human provides to start

1. Write `docs/aide/vision.md` — what the product is and why
2. Write `docs/aide/roadmap.md` — how to build it in stages
3. Write `docs/aide/definition-of-done.md` — the 3-5 journeys that prove it works
4. Write `AGENTS.md` — tech stack, package structure, language standards, PR label
5. Configure GitHub repo, Projects board, report issue, branch protection
6. Run `specify init` + install extensions + run `/speckit.maqa-github-projects.setup`
7. Open 7 sessions, set `AGENT_ID`, run role commands
8. Watch the board. Unblock `needs-human` labels. Read reports. That's it.


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
