# 13: Scheduled Execution — Hourly Autonomous Loop via GitHub Actions

> Status: Active | Created: 2026-04-19
> Applies to: kardinal-promoter

---

## What this does

kardinal-promoter runs the otherness autonomous development loop on a schedule —
every hour — without any human action required. GitHub Actions wakes the agent,
the agent completes a batch (claim → implement → QA → merge → report), and exits.
The next cron tick fires an hour later.

The human's role: add vision via `/otherness.vibe-vision`, read batch reports on
issue #1, unblock genuine `[NEEDS HUMAN]` escalations. Nothing else.

---

## How it works

A workflow file (`.github/workflows/otherness-scheduled.yml`) triggers on `schedule`
and `workflow_dispatch`. It installs the otherness agent files from
`github.com/pnz1990/otherness`, configures AWS credentials via OIDC, authenticates
the gh CLI, and runs OpenCode with the standard `/otherness.run` prompt.

The agent runs the same loop as a manual session — it just runs unattended on
GitHub's infrastructure instead of a developer's machine.

---

## Credential model

**AWS (Bedrock) — OIDC, no stored keys.**

The workflow assumes IAM role `github-bedrock-key` (account 569190534191) via
GitHub's OIDC token exchange. No `AWS_ACCESS_KEY_ID` or `ANTHROPIC_API_KEY` is
stored. The role grants only Bedrock invoke permissions and is scoped to the
`pnz1990/*` GitHub org.

This is the only compliant mechanism for Isengard-managed AWS accounts. Long-lived
IAM user access keys in these accounts trigger the Amazon security key rotation
campaign.

**GitHub — PAT stored as `GH_TOKEN`.**

The built-in `GITHUB_TOKEN` cannot trigger other workflows when it pushes commits.
The agent merges PRs and expects CI to run afterward — if CI does not run, the agent
cannot verify its own work. A PAT (`GH_TOKEN`) does not have this restriction.

The PAT has `repo` and `workflow` scopes.

---

## Secrets (all set as of 2026-04-19)

| Secret | Purpose |
|--------|---------|
| `AWS_ROLE_ARN` | OIDC role ARN for Bedrock (`arn:aws:iam::569190534191:role/github-bedrock-key`) |
| `AWS_ACCOUNT_ID` | AWS account ID (`569190534191`) |
| `AWS_DEFAULT_REGION` | Bedrock region (`us-east-1`) |
| `GH_TOKEN` | PAT with `repo` + `workflow` scopes — push, PR, review, issue, CI trigger |

---

## Cadence

| Setting | Value |
|---------|-------|
| Cron | `0 */6 * * *` — every 6 hours (steady-state standby) |
| Manual trigger | `workflow_dispatch` — available for on-demand runs |

Every 6 hours is appropriate for steady-state standby (all journeys passing, queue empty,
waiting on vision). If active development resumes (new Future items in design docs), switch
back to hourly (`0 * * * *`) by updating the cron above.

---

## Job permissions

```yaml
permissions:
  id-token: write        # AWS OIDC
  contents: write        # push commits, create/merge branches
  pull-requests: write   # open/update/merge PRs, post review comments
  issues: write          # create/label/close issues, post comments
  actions: write         # trigger CI workflows after push
  statuses: write        # post commit statuses
```

---

## Re-deploying or updating

If the workflow needs to be updated:

1. Edit `.github/workflows/otherness-scheduled.yml` on a branch
2. Open a PR — branch protection requires it
3. Merge after review

If secrets expire or need rotation:

- **AWS**: run `~/.otherness/scripts/setup-github-bedrock-key.sh --update-secrets pnz1990/kardinal-promoter`
  (re-creates OIDC role and pushes the three AWS secrets)
- **GH_TOKEN**: generate a new PAT at github.com/settings/tokens with `repo` +
  `workflow` scopes; run `echo "<new-token>" | gh secret set GH_TOKEN --repo pnz1990/kardinal-promoter`

---

## Present (✅)

- ✅ `.github/workflows/otherness-scheduled.yml` — 6-hourly cron (`0 */6 * * *`) + workflow_dispatch; Bedrock via OIDC; GH_TOKEN PAT; five job permissions (id-token, contents, pull-requests, issues, statuses) (PR #828, #834, 2026-04-19)
- ✅ `AWS_ROLE_ARN` secret set — `arn:aws:iam::569190534191:role/github-bedrock-key` (2026-04-19)
- ✅ `AWS_ACCOUNT_ID` secret set — `569190534191` (2026-04-19)
- ✅ `AWS_DEFAULT_REGION` secret set — `us-east-1` (2026-04-19)
- ✅ `GH_TOKEN` secret set — PAT with `repo` + `workflow` scopes (2026-04-19)
- ✅ Cadence reduced to `0 */6 * * *` (every 6h) — all 7 journeys passing, steady-state standby (PR #834, 2026-04-19)
- ✅ Token expiry and scope preflight step — `Validate GH_TOKEN` step checks token validity AND `repo`/`workflow` OAuth scopes via `X-OAuth-Scopes` header; posts `[NEEDS HUMAN]` issue on expired/missing-scope token (PR #836 added, PR #845 removed in rewrite, re-added PR #862, 2026-04-20)

## Future (🔲)

- ✅ **Workflow step syntax CI-validation** — the `otherness-scheduled.yml` "Install otherness agent files" step has failed repeatedly with bash syntax errors (7 consecutive runs failed 2026-04-21 00:40–06:43 UTC, see PR #943). The root cause: sequential edits by agents corrupt multi-branch if/else/fi blocks, and there is no automated check that catches this before merge. Add a CI job (or pre-merge check in `ci.yml`) that runs `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/otherness-scheduled.yml'))"` + `bash -n` on the extracted `run:` scripts. Every workflow step that contains bash should be syntax-verified before merge. A broken scheduled workflow means **the loop ships zero work** for hours without any human noticing — this is the highest-impact single reliability failure mode. (PR #943) ⚠️ NOTE: PR #943 fixed the bash syntax error. It did NOT add a `bash -n` CI guard. The `yaml.safe_load` CI step covers YAML structure only — bash errors inside `run:` blocks pass YAML validation. The actual `bash -n` CI guard is unimplemented; see the corresponding 🔲 item in doc 12 (`bash -n CI guard for otherness-scheduled.yml`).

- ✅ **PDCA coverage must never be 0/0 — flag as BROKEN when it is** — the PDCA workflow now checks `TOTAL == 0` in the `Post PDCA evidence` step: if no scenarios ran, posts `[PDCA BROKEN — no scenarios executed; workflow failed before reaching scenario step]` to Issue #1 and adds `needs-human` label to Issue #413. Normal runs (TOTAL > 0) are unaffected. (PR #1000, 2026-04-21)

- ✅ **Single-page health dashboard at Issue #1** — SM §4f-health-snapshot now maintains
  a single comment on `REPORT_ISSUE` with sentinel `<!-- otherness-health-snapshot -->`.
  Every batch: find existing sentinel comment and PATCH it; if not found, create a new one.
  Shows: loop health (GREEN/AMBER/RED), pdca status, last feat/fix PR, queue depth, update
  timestamp. Fail-open: API errors log a non-fatal warning and do not block the batch report.
  (PR #1058, 2026-04-22)

- 🔲 **Self-cadence: switch from 6h to 1h when queue is non-empty** — the cron is locked at `0 * * * *` (hourly per the security comment) but was changed to `0 */6 * * *` in PR #834 because "all 7 journeys passing, steady-state standby." The two are in conflict: the comment says hourly is required for progress, but the cron says 6h. The actual resolution: cadence should be *data-driven*: 1h when the queue has items, 6h in standby. Since GitHub Actions cron cannot be dynamic, implement this by having the session exit early (after posting a "standby" comment) if the queue is empty. This gives 6h effective cadence in standby without changing the cron, while ensuring 1h availability when work exists. Note: PR #861 reduced cadence to 6h but did NOT implement the data-driven exit-early mechanism.

- 🔲 **Issue #1 comment volume: report comments are unreadable after 50+ entries** — Issue #1 is the primary visibility surface for the otherness loop. After 50+ batch comments, it is unusable: a human must scroll through verbose `[BATCH COMPLETE]`, `[COORDINATOR]`, `[SM]`, `[VIBE-VISION-AUTO]`, and `[ANCHOR]` comments spanning multiple screens to understand the current state. The signal-to-noise ratio is too low. Implement a "rolling summary" strategy: the SM edits a single pinned comment with the last 5 batch summaries (not appends), and archives older comments to a `docs/aide/progress.md` log. Verbose per-agent comments (coord queue, engineer claims) should be written to a separate thread or label-filtered so Issue #1's default view shows only health signal, not implementation chatter.

- ⚠️ Specced-not-implemented (PR #1064, PR #1067) **Simulation predictions are now machine-readable — SM compares actuals, COORDINATOR adjusts queue size** — PR #1064 (2026-04-22): SM §4f-pred-delta reads `prs_next_batch_floor/ceiling` from `_state:sim-prediction.json` and computes `ratio = actual_merged / floor`, emitting `Sim delta: predicted N-M items, actual X (ratio: X/N)` with `⚠️ Underdelivery` when ratio < 0.5. PR #1067 (2026-04-22): SM §4f-ratio-history writes `actual_prs_merged`, `predicted_prs_floor`, and `ratio_history` (FIFO, max 5) to state after every batch. COORD §1b-delta reads the last 3 `ratio_history` entries at session start: if all 3 have ratio < 0.5, reduces `ADJUSTED_SESSION_LIMIT` by 2 (min 1); if all 3 have ratio > 1.2, increases by 1 (max 10). The simulation delta now visibly changes agent behavior — queue size shrinks when the system consistently underdelivers. (PR #1064, PR #1067, 2026-04-22) — **VERIFICATION FAILED (2026-04-23)**: PRs #1064 and #1067 changed only `spec.md` and `docs/design/12-*.md`. Neither changed `~/.otherness/agents/phases/sm.md` or `coord.md`. `state.json` on `_state` branch has no `ratio_history`, `prs_next_batch_floor`, or `ADJUSTED_SESSION_LIMIT` fields. `sim-prediction.json` does not exist in `_state`. This ✅ was produced by Scan 1 title-matching a spec-only PR — the feature is not implemented. See doc-12 `Vibe-vision Scan 1 promotes ✅ based on PR title matching alone` item.

- 🔲 **Machine-readable health comment format: embed structured data inside every SM batch report** — the SM batch report comment on Issue #1 is human-readable prose. There is no structured data block that a script or external tool can parse to extract the health signal without screen-scraping. Add an HTML comment block inside every SM batch report: `<!-- otherness-health: {"date":"...","loop":"GREEN|RED|STALL","last_feat_pr":N,"queue_depth":M,"pdca":"X/Y","consecutive_housekeeping":K} -->`. This block is invisible in the GitHub UI but parseable by `gh api` + `jq`. The single-page dashboard Future item (above) can then be implemented by reading this block from the last N comments, not by parsing prose. Without a machine-readable format, any health dashboard is fragile to prose changes across sessions. The format definition must precede the dashboard implementation.

- 🔲 **Health dashboard implementation order is not enforced: machine-readable format must ship first** — the `Single-page health dashboard` item and the `Machine-readable health comment format` item are in the same Future list but have a hard dependency: the dashboard cannot be reliably implemented without the machine-readable format. This ordering is noted in prose inside the machine-readable item but there is no gate preventing an engineer from implementing the dashboard first (producing a fragile screen-scraping implementation) and treating the format as a follow-up. Add to `team.yml` or this doc a BLOCKED-BY annotation: `Single-page health dashboard` is explicitly BLOCKED-BY `Machine-readable health comment format`. The COORDINATOR must not queue the dashboard item until the format item has a `✅`. Without the explicit BLOCKED-BY relation, the two items will be implemented in the wrong order because the dashboard is more visible and motivating. ⚠️ Inferred from pressure lens: visibility improvements will produce fragile dashboards without enforcing the dependency order.

- 🔲 **Issue #1 is not the right visibility surface: a GitHub issue's comment thread is structurally wrong for a health dashboard** — all visibility improvements (single-page dashboard, machine-readable health format, rolling summary) are being built on top of GitHub issue comments. The fundamental problem is that issue comments are not editable by the same actor who posted them via the GitHub UI (only via API), are paginated after 30 comments, cannot be pinned individually without replacing the issue description, and produce email notifications on every edit for subscribed users. A better visibility surface for "is the system healthy right now?" is: (1) the repo's `README.md` top section (updated via CI, shows last run date and loop status badge); or (2) a GitHub Actions summary badge from the scheduled workflow. The existing Future items should be re-evaluated against these surfaces before implementation. The COORDINATOR should not queue the `Single-page health dashboard` item until this surface question is resolved — building it on issue comments may produce a technically correct but operationally unusable artifact. ⚠️ Inferred from pressure lens: visibility is not good enough — a human looking at GitHub right now cannot quickly tell system health, and the proposed solution (issue comments) has structural limitations that will undermine the implementation.

- 🔲 **New project onboarding still requires human intervention: the loop is not self-bootstrapping** — a new project added today would require a human to: (1) understand otherness-config.yaml schema (undocumented); (2) set up the OIDC IAM role (requires running `setup-github-bedrock-key.sh`, which is not in the guide); (3) manually create 7+ required GitHub secrets; (4) create design doc stubs (not automatically generated); (5) seed the initial queue (not automatically generated from design docs). The `/otherness.onboard` command generates scaffolding but the human must still perform 5 distinct manual steps before the first run succeeds. The metric for "good enough" onboarding is: a human who has never seen otherness before can get a new project to its first successful scheduled run in under 30 minutes following only the generated documentation, with no external context required. This metric is not currently met and is not tracked. Until it is tracked (e.g., `onboard_started_at` + `first_run_succeeded_at` timestamps), it cannot improve. The onboarding gap is distinct from the "Otherness setup guide completeness audit" item (which audits existing docs) — this item is about making the generated scaffolding self-sufficient. ⚠️ Inferred from pressure lens: onboarding is not good enough — a new project added today would still require significant human intervention.

- 🔲 **README has no loop health badge — a repository visitor sees zero signal about system health** — `README.md` has a logo, a tagline, links to docs, and a feature description. It has no badge showing: whether the scheduled workflow is currently passing, when it last ran, or what the loop health state is. A platform engineer evaluating the project clicks the GitHub repo link, sees no CI badge, no "last run" indicator, and cannot distinguish an actively maintained project from an abandoned one. GitHub Actions provides a native badge URL: `https://github.com/pnz1990/kardinal-promoter/actions/workflows/otherness-scheduled.yml/badge.svg`. Add this badge (labeled "autonomous loop") to the README alongside a "last shipped" dynamic badge (updated by the SM via `shields.io/endpoint` reading a JSON file committed to the repo on each batch). This is the minimum visibility surface that a repo visitor sees before reading anything else. ⚠️ Inferred from pressure lens: "Is the visibility good enough? A human looking at GitHub right now cannot quickly tell if the system is healthy" — the README is the first page a human sees, and it currently shows nothing.

- ✅ **Onboarding first-run success is not verified — scaffolding generation ≠ working loop** — `scripts/onboard-smoke-test.sh` added with 4-check post-onboard validation: (1) `yaml.safe_load` on generated `otherness-scheduled.yml`; (2) `bash -n` on all `run:` blocks; (3) `yaml.safe_load` on `otherness-config.yaml`; (4) required secrets (`AWS_ROLE_ARN`, `GH_TOKEN`) present via GitHub API. Outputs `[ONBOARD SMOKE TEST: N/4 checks passed]` and `[ONBOARD GAP]: <description>` for each failure. Documented in `docs/quickstart.md §Verify your setup`. (PR #1102, 2026-04-22)

- 🔲 **Issue #1 visibility degrades over time: no mechanism to trim or archive the comment thread** — Issue #1 is the system's primary health and progress surface. As of 2026-04-21, it already has hundreds of comments from batch reports, coordinator queues, vision scans, and PDCA results. A human checking system health must scroll through dozens of verbose comments to find the latest status. GitHub issues do not support comment archiving or pinning individual comments. The existing Future items address adding a health dashboard and a machine-readable format. Neither addresses the raw volume problem: even with a parseable format, a human reader still sees the entire comment stream. Concrete mitigation: the SM's end-of-batch step should check `gh api repos/$REPO/issues/$REPORT_ISSUE/comments --jq length` and when the total comment count exceeds 200, open a new tracking issue (`[ARCHIVE] kardinal-promoter progress archive — batches X through Y`) and pin a link to it in Issue #1's description. Issue #1 stays current; the archive holds history. Without this, Issue #1 becomes unusable for humans within 6 months of active loop execution. ⚠️ Inferred from pressure lens: "Is the visibility good enough? Report issue comments are too verbose and technical."

- 🔲 **Scheduled workflow has no alerting if it has not run in >12 hours** — the cron is `0 */6 * * *`. If the cron silently fails (GitHub Actions outage, billing limit reached, invalid YAML) the system can go 12+ hours without a run and no human will notice. Issue #1 comments stop appearing, but there is no proactive alert. A human notices only when they happen to look at Issue #1 or the Actions tab. Add a lightweight canary check: a separate, minimal workflow (`otherness-watchdog.yml`) scheduled at `30 */6 * * *` (30 minutes after the main cron) that: (1) checks the `otherness-scheduled.yml` last run time via `gh run list`; (2) if the last run is >8 hours ago, posts `[WATCHDOG] otherness-scheduled has not run in >8h — last run: <timestamp>` to Issue #1 and creates a `needs-human` issue. The watchdog workflow is deliberately minimal (no otherness dependency) so it survives a broken main workflow. Without this, the loop can go dark for days without anyone being alerted — the visibility gap is worse than "hard to read" — it is "invisible stopped state." ⚠️ Inferred from pressure lens: "A human looking at GitHub right now cannot quickly tell if the system is healthy" — the worst case is not slow reporting but no reporting at all.

- 🔲 **`otherness-config.yaml` schema is undocumented — every new project requires reading the source** — the `otherness-config.yaml` file is the primary configuration artifact for a new otherness project. Its fields (`maqa.agents_path`, `maqa.report_issue`, `maqa.pr_label`, `schedule.vibe_vision_step`, etc.) have no published schema, no descriptions, and no defaults table. A new user onboarding a project must either: (a) copy from kardinal-promoter and guess which fields to change; or (b) read `otherness-config.yaml` parsing code across multiple agent files to infer what each field does. `/otherness.onboard` generates a config from a template but does not explain what each field controls. Add `docs/aide/otherness-config-schema.md`: a table with every field name, its type, its default (if any), what it controls, and the consequence of getting it wrong. This is the single document a new user most needs before running `/otherness.onboard` and it does not exist. ⚠️ Inferred from pressure lens: "A new project added today would still require significant human intervention — the setup guide is incomplete."

- 🔲 **Backlog trend (growing vs shrinking) is invisible from any single human-visible artifact** — a human who wants to know whether the system is making net progress has no single place to look. The SM batch report shows `queue_depth` (items currently queued) but not `future_backlog` (total unqueued `🔲` items across `docs/design/`). A queue depth of 2 can mean "nearly done" or "only 2 of 73 items have been surfaced." The backlog count in doc 12 is prose: "73 items as of 2026-04-22" — stale by definition. The health snapshot (PR #1058) shows loop health and PDCA but not backlog trend. Without a trend signal, a human cannot distinguish a shrinking system (delivering faster than vision adds) from an overflowing one. Concrete gap: add `future_backlog: N (trend: +/-X from last batch)` to the SM batch report AND to the health snapshot comment. The trend requires storing the previous count in `state.json` (`future_backlog_prev`) and computing the delta each batch. A consistently positive trend (`+2`, `+3`, `+1` across 5 batches) is a system health signal as important as CI pass rate: it means the loop is accumulating debt, not reducing it. Without this signal, the loop can report GREEN (PRs merging, PDCA passing) while the backlog grows 1–3 items per run — a slow-motion graveyard problem invisible to human oversight. ⚠️ Inferred from pressure lens: "Is the visibility good enough? A human looking at GitHub right now cannot quickly tell if the system is healthy" — backlog trend is the missing second dimension of health that distinguishes a productive loop from a busy-but-diverging one.

- 🔲 **Public docs site has no loop health signal — an evaluator sees product docs but not system health** — `pnz1990.github.io/kardinal-promoter/` is the public face of the project. A platform engineer evaluating kardinal reads the feature table, the comparison page, and the quickstart. Nowhere on the site does a visitor see whether the autonomous development loop is currently healthy, what shipped last, or whether the project is in active development. This is a missed trust signal: a project whose docs site shows "last shipped: 2 hours ago | loop: GREEN | PDCA: 7/7" communicates living software. A project with no loop signal looks abandoned or unmaintained. Concrete implementation: (1) the SM's end-of-batch step writes `docs/loop-status.json` with `{"loop":"GREEN","pdca":"7/7","last_feat_pr":"#NNN","updated":"YYYY-MM-DDTHH:MM:SSZ"}`; (2) the docs CI picks up this file and renders a status banner on the home page (`/_includes/loop-status.html`); (3) the banner shows: `Autonomous loop: GREEN | Last shipped: #NNN | PDCA: 7/7 | Updated: X ago`. The banner is deliberately minimal — it is not a dashboard, it is a trust signal for external evaluators. Without it, the site looks like documentation for software that may or may not still be developed. ⚠️ Inferred from pressure lens: "Is the visibility good enough? A human looking at GitHub right now cannot quickly tell if the system is healthy" — the public docs site is the highest-traffic surface and currently shows zero loop health signal.

- 🔲 **Model diversity as a structural monoculture break: ADVERSARY must use a different inference provider when available** — the ADVERSARY role (`scripts/adversary-check.sh`, PR #1104) runs the same model (Claude Sonnet via Amazon Bedrock) as all other agent roles. An ADVERSARY that uses the same model applies a different prompt frame but the same underlying reasoning patterns, training biases, and blind spots. The monoculture problem requires structural diversity, not just role diversity. When `otherness-config.yaml` includes `adversary.alt_provider: openai` (or any non-Bedrock endpoint), the ADVERSARY evaluation step should route to that provider. If no alt provider is configured, the current behavior is acceptable but the limitation must be documented in the adversary output: `[ADVERSARY NOTE: running on same model — structural bias not eliminated; configure adversary.alt_provider for true diversity]`. This surfaces the limitation rather than hiding it. Without this, the ADVERSARY role creates the illusion of diverse evaluation while providing only prompt-frame diversity. The flat DAG compilation failure (AGENTS.md canonical example) would not have been caught by a same-model ADVERSARY — it was caught by a human with different training. ⚠️ Inferred from pressure lens: "What would genuinely break the frame-lock?" — role switching on the same model is frame diversity, not model diversity; only structural provider separation produces true monoculture resistance.

- 🔲 **Self-improvement north-star: define what "meaningfully smarter" looks like so improvement can be verified** — the system has mechanisms to track skill growth (skills-inventory.md), flag ineffective skills (A/B benchmark item), and identify which error classes recur most (skills prioritization item). None of these answers: "Is the system measurably smarter than it was 30 days ago, and how would we know?" The north-star metric for self-improvement is not skill count or last-added date — it is batch failure rate reduction. Define and track: `batch_first_attempt_success_rate` = (items that shipped on the first engineer claim without retry, rollback, or `[NEEDS HUMAN]`) / (total items attempted). If this rate improves over 30 days of skill additions, self-improvement is real. If it is flat, the skills library is growing but not being applied. Add to the SM batch report: compute this rate from the last 10 batches using issue/PR comments (look for `[RETRY]`, `[ROLLBACK]`, `[NEEDS HUMAN]` tags against total closed items). Emit `[SELF-IMPROVEMENT SIGNAL: first-attempt success rate = X% (30d trend: +/-Y%)]`. When the trend is flat for 3+ batches: post `[SELF-IMPROVEMENT STALLED — skills growing but success rate not improving; run /otherness.learn on the most recent retry pattern]`. Without a north-star metric, self-improvement is a collection of mechanisms with no verified outcome — and the pressure lens question "are agents meaningfully smarter?" will always be unanswerable from any artifact in the repo. ⚠️ Inferred from pressure lens: "Is the self-improvement real?" — the system tracks skill count but not whether skills produce better first-attempt outcomes; a skill that is never applied and a skill that eliminates a recurring failure class both show as `skills: N+1` in the batch report.

---

## Zone 1 — Obligations

**O1 — Never replace OIDC with stored AWS keys.**
The IAM role `github-bedrock-key` is the only compliant credential mechanism for
this account. Do not create IAM users with access keys as a shortcut.

**O2 — Never replace `GH_TOKEN` with `GITHUB_TOKEN` for the checkout token.**
`GITHUB_TOKEN` pushes are inert — they do not trigger CI. The CI gate is the agent's
only correctness signal. Breaking it silently corrupts the loop.

**O3 — All five job permissions must be present.**
Each permission corresponds to a specific agent action (id-token for OIDC, contents for push,
pull-requests for PR creation, issues for issue comments, statuses for commit status).
Removing any one breaks that action without an obvious error message.

---

## Zone 2 — Implementer's judgment

- Model: `amazon-bedrock/global.anthropic.claude-sonnet-4-6` — same as manual sessions.
- Runner: `ubuntu-latest` — no special hardware needed.
- `fetch-depth: 0` on checkout — required for the agent to read git history for state.
- The `workflow_dispatch` trigger is kept permanently — useful for testing fixes
  and on-demand runs without waiting for the next cron tick.

---

## Zone 3 — Scoped out

- Running multiple parallel scheduled workflows (the distributed lock handles
  collisions, but multiple runners per tick wastes tokens unnecessarily)
- Self-managing cadence based on queue depth (future possibility; not implemented)
- Automatic secret rotation (PATs are long-lived; rotation is a manual operation)
