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

- 🔲 **PDCA coverage must never be 0/0 — flag as BROKEN when it is** — the PDCA workflow post to Issue #1 currently shows `PASS=0 FAIL=0` ("no results") when an upstream step fails before scenarios run. This looks like "no scenarios ran" rather than "the workflow itself is broken". Add a check: if `TOTAL == 0`, post `[PDCA BROKEN — no scenarios executed; workflow failed before reaching scenario step]` and label Issue #413 with `needs-human`. Zero coverage must be visible as an error, not a neutral "no results" message. A system that silently reports 0/0 gives humans false confidence that validation is running. Note: PR #848 added the ANCHOR coverage line but did NOT add the `BROKEN` guard for TOTAL==0.

- 🔲 **Single-page health dashboard at Issue #1** — a human looking at Issue #1 right now cannot quickly tell if the system is healthy, what it shipped today, and whether it is moving toward the vision or spinning. The report comments are verbose and require reading 20+ comments to form a picture. Add a pinned comment (updated by every session) with a machine-readable health block: last run status (PASS/FAIL), last PR merged (title + number), queue depth, days since last meaningful feature PR, PDCA coverage percentage. Template: `[HEALTH | kardinal-promoter | <date>] loop=GREEN|RED|STALL | last_pr=#NNN "<title>" | queue=N | pdca=X/Y (Z%)`. The SM should update this block every batch using `gh issue comment --edit`.

- 🔲 **Self-cadence: switch from 6h to 1h when queue is non-empty** — the cron is locked at `0 * * * *` (hourly per the security comment) but was changed to `0 */6 * * *` in PR #834 because "all 7 journeys passing, steady-state standby." The two are in conflict: the comment says hourly is required for progress, but the cron says 6h. The actual resolution: cadence should be *data-driven*: 1h when the queue has items, 6h in standby. Since GitHub Actions cron cannot be dynamic, implement this by having the session exit early (after posting a "standby" comment) if the queue is empty. This gives 6h effective cadence in standby without changing the cron, while ensuring 1h availability when work exists. Note: PR #861 reduced cadence to 6h but did NOT implement the data-driven exit-early mechanism.

- 🔲 **Issue #1 comment volume: report comments are unreadable after 50+ entries** — Issue #1 is the primary visibility surface for the otherness loop. After 50+ batch comments, it is unusable: a human must scroll through verbose `[BATCH COMPLETE]`, `[COORDINATOR]`, `[SM]`, `[VIBE-VISION-AUTO]`, and `[ANCHOR]` comments spanning multiple screens to understand the current state. The signal-to-noise ratio is too low. Implement a "rolling summary" strategy: the SM edits a single pinned comment with the last 5 batch summaries (not appends), and archives older comments to a `docs/aide/progress.md` log. Verbose per-agent comments (coord queue, engineer claims) should be written to a separate thread or label-filtered so Issue #1's default view shows only health signal, not implementation chatter.

- 🔲 **Simulation predictions are not machine-readable — SM cannot compare to actuals** — `otherness.simulate` produces a prose prediction ("I estimate 3 items will complete this batch"). The SM has no structured way to record the prediction and compare it to the actual count after the batch. Make predictions machine-readable: the COORDINATOR should write `predicted_items: N` to the session state file (`docs/aide/queue/current.json` or a new `docs/aide/metrics/last-prediction.json`). The SM reads this at batch end and computes `ratio = actual / predicted`, posting `[SIMULATION DELTA] predicted: N, actual: M, ratio: X.XX`. A ratio < 0.5 for 3 consecutive batches is a calibration alert. Without machine-readable predictions, the "loop prediction → behavior feedback loop" Future item in doc 12 cannot be implemented.

- 🔲 **Machine-readable health comment format: embed structured data inside every SM batch report** — the SM batch report comment on Issue #1 is human-readable prose. There is no structured data block that a script or external tool can parse to extract the health signal without screen-scraping. Add an HTML comment block inside every SM batch report: `<!-- otherness-health: {"date":"...","loop":"GREEN|RED|STALL","last_feat_pr":N,"queue_depth":M,"pdca":"X/Y","consecutive_housekeeping":K} -->`. This block is invisible in the GitHub UI but parseable by `gh api` + `jq`. The single-page dashboard Future item (above) can then be implemented by reading this block from the last N comments, not by parsing prose. Without a machine-readable format, any health dashboard is fragile to prose changes across sessions. The format definition must precede the dashboard implementation.

- 🔲 **Health dashboard implementation order is not enforced: machine-readable format must ship first** — the `Single-page health dashboard` item and the `Machine-readable health comment format` item are in the same Future list but have a hard dependency: the dashboard cannot be reliably implemented without the machine-readable format. This ordering is noted in prose inside the machine-readable item but there is no gate preventing an engineer from implementing the dashboard first (producing a fragile screen-scraping implementation) and treating the format as a follow-up. Add to `team.yml` or this doc a BLOCKED-BY annotation: `Single-page health dashboard` is explicitly BLOCKED-BY `Machine-readable health comment format`. The COORDINATOR must not queue the dashboard item until the format item has a `✅`. Without the explicit BLOCKED-BY relation, the two items will be implemented in the wrong order because the dashboard is more visible and motivating. ⚠️ Inferred from pressure lens: visibility improvements will produce fragile dashboards without enforcing the dependency order.

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
