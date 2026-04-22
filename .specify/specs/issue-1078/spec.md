# Spec: issue-1078 — Otherness onboarding quality gate: post-onboard smoke test

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: **Otherness onboarding quality gate: `/otherness.onboard` output review** (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: A script `scripts/onboard-smoke-test.sh` exists and is executable. It accepts
one argument: the path to the project root to validate (defaults to `$PWD`).

**O2**: The script runs these 4 checks and reports PASS/FAIL for each:
1. YAML structure: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/otherness-scheduled.yml'))"` — workflow YAML is parseable
2. Bash syntax: extracts each `run:` block from the workflow and runs `bash -n` on it
3. Config syntax: `python3 -c "import yaml; yaml.safe_load(open('otherness-config.yaml'))"` — config YAML is parseable
4. Secrets: uses `gh api repos/$REPO/actions/secrets --jq '[.secrets[].name]'` to check that required secret names (`AWS_ROLE_ARN`, `GH_TOKEN`) are present

**O3**: The script prints a summary line: `[ONBOARD SMOKE TEST: N/4 checks passed]`. If N < 4, each failing check is reported as `[ONBOARD GAP]: <description>`.

**O4**: The script exits with code 0 if all checks pass, 1 if any check fails.

**O5**: The script is referenced in `docs/quickstart.md` under a "Verify your setup" section.

---

## Zone 2 — Implementer's judgment

- The script should be self-contained bash (no Python required for the core flow)
- Secret check uses the GitHub API — requires `GH_TOKEN` or `GITHUB_TOKEN` env var
- If the required secrets API call fails (no auth), downgrade the secret check to a warning
- The `bash -n` check on extracted run blocks should fail gracefully if PyYAML is not installed
- This script can be run by a human after `/otherness.onboard` to verify setup before the first run

---

## Zone 3 — Scoped out

- Modifying `~/.otherness/agents/onboard.md` (requires pushing to otherness upstream repo)
- Auto-invocation of this script from onboard.md (separate upstream change)
- Checking all possible required secrets (only check the 2 most critical: AWS and GH_TOKEN)
