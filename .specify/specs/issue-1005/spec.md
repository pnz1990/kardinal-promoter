# Spec: issue-1005 — PM §5j version staleness check

## Design reference
- **Design doc**: `docs/design/41-published-docs-freshness.md`
- **Section**: `§ Future — Version string freshness`
- **Implements**: Version string freshness — PM §5j version staleness check (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable, must satisfy)

1. **O1 — Script exists**: `scripts/version-staleness-check.sh` is present, executable,
   and has the Apache 2.0 copyright header.

2. **O2 — Version extraction**: The script reads the latest git tag using
   `git tag --list 'v*' | sort -V | tail -1`. If no tag exists, the script exits 0
   with a "[SKIP]" message (fail-open).

3. **O3 — Version string scan**: The script scans `README.md` and `docs/comparison.md`
   (if present) for version strings matching the regex `v[0-9]+\.[0-9]+\.[0-9]+`.
   All matches are extracted and deduplicated.

4. **O4 — Staleness comparison**: For each found version string, if it is older than
   the latest git tag by ≥1 minor version (e.g. found `v0.5.0`, latest is `v0.6.0`),
   it is flagged as stale.

5. **O5 — Issue creation**: For each stale version string found, the script opens
   a `kind/docs,priority/high` GitHub issue titled:
   `docs: stale version string in <filename>: found <found_ver>, latest is <latest_ver>`
   Dedup guard: if an issue with this title already exists (open), skip.

6. **O6 — Fail-open**: Any error (gh CLI unavailable, no README, no git tags) causes
   the script to exit 0 with a skip message — never blocks the SM.

7. **O7 — Idempotent**: Running the script multiple times without a version bump
   does not create duplicate issues (dedup guard enforces this).

---

## Zone 2 — Implementer's judgment

- Version comparison algorithm: parse `vMAJOR.MINOR.PATCH` tuples. "Stale by ≥1 minor"
  means the found MINOR < latest MINOR (when MAJOR is the same), or found MAJOR < latest MAJOR.
- File list to scan: `README.md` and `docs/comparison.md`. Extensible but not required
  to scan all docs files (would be too noisy).
- Issue labels: `kind/docs,priority/high` — consistent with other PM staleness checks.
- Script takes `$REPO` and `$REPORT_ISSUE` as env vars or positional args, consistent
  with existing scripts (`zero-pr-detect.sh` pattern).

---

## Zone 3 — Scoped out

- Does NOT scan all docs files (only README.md and docs/comparison.md)
- Does NOT auto-fix version strings (opens issue for human review)
- Does NOT run as part of any Go code or CI workflow (it's a PM phase script called
  from the agent loop, not from CI directly)
- Does NOT validate patch versions (only major/minor staleness matters)
