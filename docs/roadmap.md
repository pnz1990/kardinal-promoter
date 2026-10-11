# Roadmap

This page lists what is planned for kardinal-promoter. For what ships today, see the
[home page](index.md) and the [comparison](comparison.md); the [changelog](changelog.md) lists
what changed in each release.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub issue](https://github.com/pnz1990/kardinal-promoter/issues) if a feature matters to your use case.

---

## Planned

- Move to kro v0.10.0 once it is released (kardinal targets v0.10.0-rc.0)
- `rollback` and `promote --env` without re-running upstream environments

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
