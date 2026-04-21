# 12: Autonomous Loop Discipline — Zero Human Dependencies in Steady State

> Status: Active | Created: 2026-04-18
> Applies to: kardinal-promoter agent sessions

---

## What this does

Defines the loop behavior that eliminates human-gated PR queues. The agent must
merge everything it produces without waiting. Human involvement is only valid for
three specific scenarios — everything else the agent resolves autonomously.

---

## The three valid `[NEEDS HUMAN]` scenarios

Everything outside these three is an agent failure, not a design constraint:

1. **Token has no admin rights** — the GitHub token cannot modify branch protection.
   Symptom: 403 on `PATCH repos/{owner}/{repo}/branches/main/protection`.
   Fix: configure the token with admin rights. Until fixed: post once, do not repeat.

2. **Genuine value judgment required** — the agent cannot determine correctness
   without product knowledge the human hasn't encoded yet. Example: two competing
   architectural approaches where both are valid and the choice has long-term implications.
   This is rare. Most "I'm not sure" situations are solvable with more reading.

3. **CI has been red for >24 hours and the root cause requires external access** —
   the agent cannot diagnose a failing CI step because it requires access to a
   credential, external service, or environment the agent cannot reach.

Everything else — merge conflicts, branch protection, test failures, spec ambiguity,
review requirements — the agent resolves autonomously.

---

## Autonomous merge protocol (in priority order)

When `gh pr merge $PR_NUM --squash --delete-branch` fails:

### Step 1 — Try `--admin`
```bash
gh pr merge $PR_NUM --repo $REPO --squash --delete-branch --admin
```
If this fails with "Pull request is not mergeable": check CI, fix, retry.
If this fails with "Must be approved by someone other than the author" or similar:
proceed to Step 2.

### Step 2 — Disable `enforce_admins`, merge, re-enable
```bash
# Disable — returns 200 if token has admin rights, 403 if not
RESULT=$(gh api -X PATCH "repos/$REPO/branches/main/protection" \
  --field enforce_admins=false 2>&1)
if echo "$RESULT" | grep -q '"enforce_admins":.*false\|200'; then
  gh pr merge $PR_NUM --repo $REPO --squash --delete-branch --admin
  # Always re-enable, even on merge failure
  gh api -X PATCH "repos/$REPO/branches/main/protection" --field enforce_admins=true
else
  echo "[QA] Cannot disable enforce_admins (403) — token lacks admin rights"
  # This is valid [NEEDS HUMAN] scenario 1. Post once and move on.
fi
```

### Step 3 — If branch protection requires a specific reviewer account

This means the project requires a second GitHub account. This is valid scenario 1.
Post `[NEEDS HUMAN: merge-blocked — branch protection requires reviewer with write access.
Token: $(gh api user --jq .login). Branch protection: require_code_owner_reviews=$(gh api
repos/$REPO/branches/main/protection --jq .required_pull_request_reviews.require_code_owner_reviews)]`

Do not post `[NEEDS HUMAN]` for any other merge failure scenario.

---

## Queue generation discipline

When the agent is about to generate a new queue, it first checks:

```bash
# Count in-review items that the agent cannot currently merge
BLOCKED_COUNT=$(python3 -c "
import json
s = json.load(open('.otherness/state.json'))
print(len([d for d in s.get('features',{}).values() 
          if d.get('state') == 'in_review']))
")
```

If `BLOCKED_COUNT >= 3`: do not generate new work. Attempt to merge existing
in-review items using the autonomous merge protocol. If all are genuinely unmergeable
(scenario 1), enter standby. Do not generate more work that will also be unmergeable.

---

## Present (✅)

- ✅ **qa.md §3e — 3-step autonomous merge protocol** (`_merge_pr()` function):
  normal → `--admin` → clear branch protection (PUT) → restore. Never posts
  `[NEEDS HUMAN: pr-approval-required]` before attempting all three steps.
  `_RESTORE_TRAP` with `trap ... EXIT` ensures branch protection is always restored.
  Implemented in `~/.otherness/agents/phases/qa.md §3e`.

- ✅ **qa.md §3e — enforce_admins toggle error handling**: The `_RESTORE_TRAP`
  environment variable captures the restore curl command; `trap "$_RESTORE_TRAP" EXIT`
  fires on both normal exit and error. Explicit `eval "$_RESTORE_TRAP"` + `trap - EXIT`
  on normal exit. Implemented alongside the 3-step protocol in qa.md §3e.

- ✅ **coord.md §1c — queue gate (in_review ≥ 3)**: When total `in_review` items ≥ 3,
  queue generation is skipped and the queue-gen lock is released immediately. Prevents
  saturating the review queue when items accumulate. Implemented in coord.md §1c.

- ✅ **standalone.md HARD RULES — `[NEEDS HUMAN]` as last resort**: The HARD RULES
  section states: "Before posting `[NEEDS HUMAN]` for any merge failure: attempt all 3
  steps in qa.md §3e. Only post `[NEEDS HUMAN]` when step 3 fails with a specific error
  (403 = no admin rights). Log the exact error." Implemented in standalone.md §HARD RULES.

## Future (🔲)

- 🔲 **Loop honesty signal: "housekeeping-only" PR detection** — the SM health signal reports GREEN when CI passes and PRs merge. But a batch can consist entirely of `chore(coord):` or `state:` commits — zero real feature content. A truly reliable loop ships at least one substantive PR (feat/fix/test/docs) every N runs. Add a loop health metric: count of consecutive runs where the only merged content was housekeeping (chore/state/coord/vision). If this count exceeds 3, the SM must post `[LOOP STALL — N consecutive housekeeping-only runs]` and the COORDINATOR must escalate to vision scan to add new queue items. Define "substantive": a PR that changes files outside `.otherness/`, `docs/`, `.github/state/`.

- 🔲 **Self-improvement tracking: skills library growth rate** — `otherness.learn` is the mechanism for agents to encode new patterns as skills. It runs rarely; there is no metric tracking how many skills exist or when the last one was added. Add to the SM batch report: `skills: N (last_added: <date>)`. If no skill has been added in 14 days, the SM should flag it. The monoculture problem (all agents share the same reasoning framework) will not be addressed without a systematic forcing function that requires new skills to be extracted from every batch that surfaces a novel pattern.

- 🔲 **Loop prediction → behavior feedback loop** — the simulation exists (`otherness.simulate`) but its predictions are not visibly changing agent behavior. The SM should compare the predicted item completion count from the last simulation against actual items completed. If actual < predicted * 0.5 consistently, something is miscalibrated. Post the delta in every SM batch report: `predicted: N items, actual: M items (ratio: M/N)`. Persistent underdelivery is a system signal, not a one-off.

- 🔲 **Workflow self-healing: detect and recover from syntax-error regression** — when the scheduled workflow fails at a pre-agent step (Install/Checkout/Auth) for N consecutive runs, the system has no mechanism to detect or recover. The agent never runs, so it cannot self-diagnose. Add an out-of-band canary: a lightweight daily GitHub Actions check (separate, minimal workflow with no otherness dependency) that: (1) runs `bash -n` on each `run:` block in `otherness-scheduled.yml`; (2) posts a `[WORKFLOW BROKEN]` comment to Issue #1 if any syntax check fails; (3) opens a `needs-human` issue with the failed step name and the bash error. This creates a separate watchdog that survives a broken main workflow.

- 🔲 **Otherness onboarding quality gate: `/otherness.onboard` output review** — a new project added via `/otherness.onboard` produces docs (otherness-config.yaml, workflow YAML, design doc stubs) that still require manual editing before the loop runs successfully. The onboarding is "close but not zero-touch." The gap is systematic: the tool generates the scaffolding but does not validate it (no `bash -n` on the generated workflow, no `gh api` call to verify the generated secret names exist, no dry-run of the first scheduled run). Add a post-onboard validation step to the onboard agent: after generating all files, run the same preflight checks the workflow would run, and surface any gaps as `[ONBOARD GAP]` items the human must fix. The human should see a clear "3 gaps found, fix these before your first run" summary, not discover them after the first failed scheduled run.

- 🔲 **SM health state definition: explicit thresholds for GREEN/RED/STALL** — the SM posts a health signal after each batch but the criteria for GREEN vs RED vs STALL are implicit and subjective. A batch where CI passes but no feature PR merged can still be reported as GREEN. Define precise, machine-checkable thresholds: GREEN = at least one feat/fix/test/docs PR merged AND PDCA ≥ 80%; STALL = 3+ consecutive runs with zero substantive PRs; RED = workflow failed (no agent ran) OR PDCA = 0. These thresholds should be defined in `team.yml` (or this doc) and enforced by the SM `health_check` step rather than being judgment calls. Without explicit thresholds, the health signal is noise — it does not distinguish a productive run from a housekeeping-only run.

- 🔲 **Monoculture break: adversarial agent role for architecture reviews** — all sessions (COORDINATOR, ENGINEER-1..3, QA, SM, PM) share the same model and reasoning framework. When a design decision is made, it is reviewed by agents that reason identically to the one that made it. This is the monoculture problem: a systematic bias in one session propagates undetected. The flat DAG compilation failure (described in AGENTS.md) is the canonical example. Add an `ADVERSARY` session role whose sole function is to find the failure mode of any proposal before it is queued as an implementation item. The ADVERSARY asks "what would break this?" before COORDINATOR adds the item to the queue. Even if the ADVERSARY is the same model, the role prompt forces a different evaluation frame. The ADVERSARY session should be lightweight — it does not implement, it only challenges.

- 🔲 **Zero-PR session detection: agent ran but produced no mergeable content** — the SM currently detects housekeeping-only PRs but not the case where the agent ran for its full token budget and produced zero PRs at all (e.g., all items failed CI, all PRs were rejected by the merge protocol, or the coordinator generated a queue but no engineer picked up work). This is distinct from "session stall" — the workflow ran, the agent ran, tokens were consumed, but nothing shipped. Add detection: after each batch, if `gh pr list --merged --search "created:>1h ago"` returns 0 items, post `[SESSION DRY RUN — agent ran but shipped 0 PRs]` to Issue #1. Persistent dry runs (3+) should escalate to `[NEEDS HUMAN]` since the queue or merge protocol may be stuck in a way the agent cannot self-diagnose.

- 🔲 **Onboarding time-to-first-run metric: track and publish the setup duration** — the onboarding guide claims setup takes "under 30 minutes" but this is not measured. A new project added via `/otherness.onboard` has no record of how long it took from `gh repo create` to first successful scheduled run. Add a timestamp to the onboard output (`onboard_started_at`, `first_run_succeeded_at`) written to `otherness-config.yaml` or a `docs/design/00-onboarding.md` doc. The SM should report `time_to_first_run: Xmin` in the first 3 batch reports after a new project is onboarded. Without this metric, the claim "onboarding takes 30 minutes" is unverifiable and the improvement pressure from the vision scan has no baseline to push against.

- 🔲 **Self-feeding queue: COORDINATOR auto-triggers vision scan when queue empties** — when the work queue reaches zero items, the system currently stalls and waits for a human to run `/otherness.vibe-vision` or manually add direction. This is a reliability gap: a truly reliable loop ships at least one meaningful PR every run without exception, which requires work to always exist. When the COORDINATOR detects an empty queue at batch start, it should: (1) scan `docs/design/*/## Future` for unqueued `🔲` items; (2) convert the top N items (by document recency and priority) directly into queue items without human intervention; (3) if no Future items exist, invoke the vision scan agent inline to generate them. The human's role is to set *direction* (via vision pressure), not to manually re-seed the queue each time it empties. Without this, the loop's throughput is bounded by human attention span rather than queue depth.

- 🔲 **`otherness.learn` automatic invocation: skills extracted from every novel-pattern batch** — `otherness.learn` is designed to encode reusable patterns as skills, but it runs only when a human explicitly invokes it. This means most novel patterns (e.g., a new krocodile workaround, a new merge-conflict resolution, a new CEL expression pattern) are discovered in one session and lost in the next. The SM's end-of-batch step should check: "did this batch surface a pattern not present in any existing skill?" If yes, automatically invoke `otherness.learn` on the relevant artifact (PR diff, session log excerpt, error resolution). The SM does not need to judge what's "novel" — it can run `otherness.learn` on every batch where a non-trivial feat/fix merged, letting the skill agent decide whether to encode it. Without automatic invocation, the skills library grows at a rate of zero-per-batch unless a human explicitly notices and triggers it.

- 🔲 **Otherness setup guide completeness audit** — the setup guide (wherever it lives — `/otherness.onboard` output, `docs/`, otherness wiki) has not been independently audited for completeness. A new user following it step-by-step would encounter gaps: the OIDC role setup requires `setup-github-bedrock-key.sh` which is not in the guide; the `otherness-config.yaml` schema is not documented; the `team.yml` `agents_path` path-security rule is not mentioned. The gap: the guide was written to match one project's setup, not generalized for arbitrary new projects. Audit by simulating a cold-start on a fresh repo (not kardinal-promoter): follow every step in the guide exactly and record every point where the guide says "X" but the real system requires "Y". File each gap as a `[ONBOARD GAP]` item. Without this audit, "new project added today would still require significant human intervention" remains true regardless of how many quality-gate Future items are implemented.

---

## Zone 1 — Obligations

**O1 — The autonomous merge protocol is attempted before any `[NEEDS HUMAN]` post.**
No PR may be labeled `[NEEDS HUMAN: pr-approval-required]` until the agent has tried:
(a) `--admin` merge, (b) enforce_admins disable + merge. If (b) fails with 403, that
is valid scenario 1. Log the specific HTTP error in the [NEEDS HUMAN] post.

**O2 — `[NEEDS HUMAN]` posts are unique per PR, not per cycle.**
If the agent already posted `[NEEDS HUMAN]` for PR #N in a previous cycle, it does
not post again in the next cycle. It checks existing comments before posting.

**O3 — Queue generation is gated by in-review count.**
If in-review items >= 3 and none can be merged (scenario 1 confirmed), no new
work items are generated. The session enters standby.

**O4 — enforce_admins is always restored.**
Any session that sets enforce_admins=false must set it back to true before the
bash block exits — including on failure. This is non-negotiable.

---

## Zone 2 — Implementer's judgment

- Whether to gate at 3 or a different number: 3 is correct for single-operator projects.
  A human typically reviews 1-3 PRs per sitting. Beyond 3 queued, they stop reviewing.
- Whether to apply this to all PR types: yes. There is no "trivial" PR that justifies
  a different merge strategy.
- Whether the agent should comment on PRs to indicate they're waiting: no. The
  `[NEEDS HUMAN]` issue label is sufficient. Comments create noise.

---

## Zone 3 — Scoped out

- Projects with multiple required reviewers (enterprise branch protection)
- Merge queue workflows (branch protection using merge queues instead of direct merge)
- Dependabot PRs (the agent does not handle automated dependency PRs)
