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

### P4 — Onboarding: a new project must not need significant human intervention

- 🔲 **`/otherness.onboard` produces drafts that need manual editing before the loop can run** — The onboard agent writes `docs/aide/vision.md`, `docs/aide/roadmap.md`, `docs/aide/definition-of-done.md`, and `.otherness/state.json` as drafts. These drafts require human editing before `/otherness.run` can operate meaningfully: the vision is generic, the DoD items are inferred from code surface rather than actual product journeys, and the state.json has no claimed items to start from. A new project added today would still take 30–60 minutes of human editorial work before the first batch is useful. The onboard agent must produce docs that are immediately runnable: (a) vision.md must include at least 3 specific, testable user journeys inferred from `README.md` and `docs/quickstart.md`; (b) definition-of-done.md must have at least 1 journey with a concrete `kubectl` validation command; (c) state.json must seed 3 starter work items from the inferred vision. After these changes, the human's only required action is `gh secret set` for the credentials — not editorial review.

- 🔲 **The `otherness-config.yaml` setup guide is incomplete for new adopters** — `~/.otherness/agents/README.md` describes what otherness does but does not provide a step-by-step setup for a new project. A developer who finds otherness and wants to add it to their Go project cannot follow a single document from zero to running first batch. The setup requires: (1) fork or add `otherness-config.yaml`, (2) add the secrets (AWS OIDC role or API key, GH_TOKEN), (3) add the workflow file, (4) run `/otherness.onboard`, (5) merge the onboard PR, (6) trigger the first scheduled run. Steps 1–3 have no single canonical source of truth — they are spread across `README.md`, `docs/design/13-scheduled-execution.md`, and agent comments. Write a `SETUP.md` in `~/.otherness/` (or update `README.md`) that is a complete, ordered checklist covering all 6 steps. This is the document a new adopter needs to exist.

- 🔲 **No validation step after `/otherness.onboard`** — After the onboard agent creates the PR, neither the agent nor the CI validates that the produced `docs/aide/` files are sufficient to run the first batch. Common silent failures: `state.json` missing required fields, `vision.md` with no concrete journeys, `definition-of-done.md` with no validation commands. Add a `validate-onboard-output` step to the onboard agent that, before opening the PR, runs a Python check on the produced files and fails loudly if: (a) `state.json` has `features: {}`, (b) `vision.md` has fewer than 200 words (too short to be actionable), (c) `definition-of-done.md` has no `kubectl` commands. This surfaces onboard quality issues before the human reviews the PR, not after the first batch produces nothing.

### P5 — Visibility: a human must be able to assess system health in under 60 seconds

- 🔲 **No single-page health dashboard on GitHub** — A human looking at the repository right now cannot quickly tell: (a) whether the last batch shipped a feature, (b) what the agent is working on, (c) whether the loop is healthy or spinning in circles. The batch reports on Issue #1 contain this information but require reading dozens of comments, each 30–50 lines long. Add a pinned GitHub issue (or a `docs/aide/status.md` that is auto-updated every batch) that contains exactly: current loop health (GREEN/YELLOW/RED), last `feat:` PR merged and when, current in-progress work items, consecutive empty runs, and next scheduled run time. This issue/page must be updated as the FIRST action of every batch before any other work begins. The human must be able to look at one URL and know whether the system is healthy.

- 🔲 **Batch reports on Issue #1 are too verbose for human consumption** — Each batch comment on Issue #1 is 30–100 lines of technical output: JSON diffs, git log excerpts, CI job names, PR numbers, and status codes. A human who reviews Issue #1 weekly cannot quickly extract signal from noise. Add a "TLDR" block at the top of every batch comment with exactly 3 lines: (1) "✅/⚠️/❌ Delivered: [N feat PRs merged] / [titles]", (2) "Next: [what the next batch will work on]", (3) "Needs: [any NEEDS HUMAN items, or 'nothing']". The verbose detail follows below the TLDR for engineers who need it. The TLDR format must be machine-parseable so the health dashboard (above) can extract it automatically.

- 🔲 **Loop health is not visible from the GitHub Actions tab** — The GitHub Actions run list for `otherness-scheduled.yml` shows `✓ completed` for every run, even runs that produced nothing or had internal errors. An operator checking the Actions tab cannot distinguish a productive run from an empty one. Add a final step to the scheduled workflow that sets a job output `delivered=true/false` and uses `gh run annotate` or a step summary to write a one-line result: "Delivered: 2 PRs merged (feat: X, fix: Y)" or "Delivered: nothing (queue empty)". This makes the GitHub Actions run history a reliable health signal rather than a green checkbox that means nothing about actual output.

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
