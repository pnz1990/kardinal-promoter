# Spec: issue-1161

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: `bash -n` CI guard for `otherness-scheduled.yml` run blocks (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `scripts/validate-workflow-bash-syntax.sh` exists and executes bash -n on each run: block.
- Verification: `test -f scripts/validate-workflow-bash-syntax.sh && bash scripts/validate-workflow-bash-syntax.sh && echo PASS`

**O2**: The script exits non-zero if any run: block has a bash syntax error.
- Verification: create a temp workflow with broken if/fi, run the script, expect exit code 1.

**O3**: The script exits 0 when all run: blocks are syntactically valid.
- Verification: `bash scripts/validate-workflow-bash-syntax.sh .github/workflows/otherness-scheduled.yml` → exit 0.

**O4**: The script is executable and self-contained (no Go changes, no external dependencies beyond bash+python3+pyyaml).
- Verification: `test -x scripts/validate-workflow-bash-syntax.sh`

**O5**: The design doc item is flipped from 🔲 to ✅.
- Verification: `grep -q '✅.*bash.*-n' docs/design/12-autonomous-loop-discipline.md`

Note: CI integration into `.github/workflows/ci.yml` is blocked by the GitHub App lacking
`workflows` installation permission. A needs-human issue is filed. The script is the
deliverable; the CI step will be wired in when permission is granted.

---

## Zone 2 — Implementer's judgment

- The script uses Python (pyyaml) for YAML parsing and subprocess for bash -n.
- Temp files are cleaned up after each check.
- The script accepts a workflow file path as argument (defaults to otherness-scheduled.yml).

---

## Zone 3 — Scoped out

- CI integration into ci.yml (blocked by workflows permission — separate needs-human issue).
- Checking other workflow files (can be added via script argument).
