# Spec: issue-1184

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future` (Lens 7 — competitive scan)
- **Implements**: **GitOps Promoter parity gap analysis is absent** (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `scripts/gitops-promoter-gap-check.sh` exists in the repository root and is executable.
- Verification: `test -f scripts/gitops-promoter-gap-check.sh && head -3 scripts/gitops-promoter-gap-check.sh | grep -q '#!/usr/bin/env bash'`

**O2**: The script fetches open `kind/enhancement` issues from `argoproj-labs/gitops-promoter` via `gh api` and cross-references against `docs/design/15-production-readiness.md` 🔲 items.
- Verification: `grep -q 'argoproj-labs/gitops-promoter' scripts/gitops-promoter-gap-check.sh`

**O3**: The script supports `--min-reactions N` (default: 3) and `--json` flags, matching the kargo-gap-check.sh interface for PM scripting consistency.
- Verification: `grep -qE '\-\-min-reactions|\-\-json' scripts/gitops-promoter-gap-check.sh`

**O4**: The script exits 0 on success (including when no gaps are found) and exits 1 on API or IO error. It does NOT exit 1 when gaps are found (this would break PM automation).
- Verification: `bash scripts/gitops-promoter-gap-check.sh --help 2>&1 || true; echo "exit $?"` (dry-run verifiable via code inspection)

**O5**: The design doc item in `docs/design/15-production-readiness.md` is flipped from 🔲 to ✅ in the same commit, with the PR number and date.
- Verification: `grep -q '✅.*gitops-promoter-gap-check' docs/design/15-production-readiness.md`

**O6**: Apache 2.0 copyright header is present in the script.
- Verification: `grep -q 'Copyright 2026 The kardinal-promoter Authors' scripts/gitops-promoter-gap-check.sh`

---

## Zone 2 — Implementer's judgment

- Reaction threshold: 3 (lower than kargo's 5) because GitOps Promoter has a smaller community; a lower threshold surfaces more signal.
- Keyword extraction: same STOPWORDS approach as kargo-gap-check.sh for consistency.
- The GitOps Promoter repo uses both `kind/enhancement` and `kind/feature` labels; check both.
- Output format must be identical to kargo-gap-check.sh (GAP #N, URL, reaction count) so PM scripts can parse both outputs uniformly.

---

## Zone 3 — Scoped out

- PM phase wiring (pm.md §5n) — that is an otherness-internal agent file; cannot be modified in this session (outside CODE zone per standalone.md).
- Automatic doc-15 update from script output — this remains a PM judgment call.
- Integration tests that actually call the GitHub API in CI — the script is used by humans/PM phase; no CI job is added.
