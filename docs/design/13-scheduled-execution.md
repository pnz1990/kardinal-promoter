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
| Cron | `0 * * * *` — every hour |
| Manual trigger | `workflow_dispatch` — available for on-demand runs |
| `otherness-config.yaml` `schedule.cron` | `0 * * * *` |

Hourly is appropriate for an active development project. If the project enters a
low-churn phase (e.g. all Future items complete, waiting on vision), consider
reducing to `0 */6 * * *` to avoid unnecessary token spend.

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

- ✅ `.github/workflows/otherness-scheduled.yml` — hourly cron (`0 * * * *`) + workflow_dispatch; Bedrock via OIDC; GH_TOKEN PAT; all six permissions (PR #828, 2026-04-19)
- ✅ `AWS_ROLE_ARN` secret set — `arn:aws:iam::569190534191:role/github-bedrock-key` (2026-04-19)
- ✅ `AWS_ACCOUNT_ID` secret set — `569190534191` (2026-04-19)
- ✅ `AWS_DEFAULT_REGION` secret set — `us-east-1` (2026-04-19)
- ✅ `GH_TOKEN` secret set — PAT with `repo` + `workflow` scopes (2026-04-19)
- ✅ `otherness-config.yaml` `schedule.cron` field — `0 * * * *` (existing)

## Future (🔲)

- 🔲 Token expiry preflight: add a step before `Run otherness` that verifies `GH_TOKEN` has required scopes; post a `[NEEDS HUMAN]` issue on the report issue (#1) if the token is expired or missing scopes, so the failure is visible rather than silent
- 🔲 Reduce cadence to `0 */6 * * *` when the project enters steady-state standby (empty queue, autonomous vision synthesizing) — update both the workflow cron and `otherness-config.yaml`

---

## Zone 1 — Obligations

**O1 — Never replace OIDC with stored AWS keys.**
The IAM role `github-bedrock-key` is the only compliant credential mechanism for
this account. Do not create IAM users with access keys as a shortcut.

**O2 — Never replace `GH_TOKEN` with `GITHUB_TOKEN` for the checkout token.**
`GITHUB_TOKEN` pushes are inert — they do not trigger CI. The CI gate is the agent's
only correctness signal. Breaking it silently corrupts the loop.

**O3 — All six job permissions must be present.**
Each permission corresponds to a specific agent action. Removing any one breaks that
action without an obvious error message.

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
