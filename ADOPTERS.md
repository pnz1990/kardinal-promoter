# ADOPTERS

This file lists known production and development users of kardinal-promoter.

To add your organization, open a pull request with a new row in the table below.

---

## Organizations Using kardinal-promoter

| Organization | Use Case | Environment | Added |
|---|---|---|---|
| kardinal-promoter (self) | Nightly PDCA workflow (`.github/workflows/pdca.yml`) — promotes `kardinal-test-app` through `kardinal-demo` (env/test → env/uat → env/prod) on a kind cluster. It has not run since 2026-07-17. | kind (single-cluster) | 2026-04-21 |

---

## Notes

The PDCA validation loop that runs on this repository is the first public adopter. It runs:

1. `kardinal create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:sha-<SHA>`
2. The bundle auto-promotes through `test` (auto) and `uat` (auto) environments
3. A PR is opened for `prod` (pr-review approval required)
4. Policy gates validate schedule, soak time, and bundle provenance

No journey in `docs/aide/definition-of-done.md` has live-cluster evidence yet; see its Journey Status table.

---

*To add your organization: open a PR with a new row. The only requirement is that the use
is genuine — development, staging, or production. Community adopters help others trust the
project is maintained and used.*
