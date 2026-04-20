# Spec 882 — Add Trivy filesystem vulnerability scan to CI (14.7)

## Design reference
- **Design doc**: `docs/design/14-v060-roadmap.md`
- **Section**: `§ Future`
- **Implements**: 14.7 — Security hardening: Trivy scan blocking on HIGH/CRITICAL CVEs (🔲 → ✅)

---

## Zone 1 — Obligations

**O1** — CI workflow (`.github/workflows/ci.yml`) contains a Trivy scan step.
Violation: no trivy step in ci.yml.

**O2** — The scan uses `trivy fs .` (filesystem scan) to check Go module dependencies
for HIGH or CRITICAL CVEs. It does NOT require a Docker image build.
Violation: step requires building a Docker image.

**O3** — The scan step blocks CI (exit-code: 1) when HIGH or CRITICAL CVEs are found.
Violation: step uses exit-code: 0 or continue-on-error: true.

**O4** — The scan ignores unfixed CVEs (ignore-unfixed: true) to avoid false positives
from CVEs with no available fix.
Violation: step doesn't use ignore-unfixed.

**O5** — The existing release.yml trivy image scan is NOT modified.
Violation: any modification to release.yml.

---

## Zone 2 — Implementer's judgment

- Where in ci.yml to place the step: after the `govulncheck` job, as a separate job.
- Whether to use aquasecurity/trivy-action or raw trivy binary installation.
  aquasecurity/trivy-action is already used in release.yml — use the same action for consistency.
- Scan target: `trivy fs --scanners vuln .` (Go modules vulnerability scan on the filesystem)

---

## Zone 3 — Scoped out

- Image scanning in PR/main CI (images are only built on release tags)
- Scanning other file types (secrets, config) — only vuln scanner needed
- Changing the release.yml scan (it already exists with exit-code: 0 for post-release reporting)
