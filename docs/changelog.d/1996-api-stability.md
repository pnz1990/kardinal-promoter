---
section: Documentation
---

- **API stability and the path to 1.0** (#1996) — a new page, [API stability](api-stability.md), covering what kardinal does today and what 1.0 needs:
  - **Stability levels.** It gives each of the 25 kinds a level, Settled, Evolving or New, based on what its schema did in v0.11, v0.12 and v0.13: fields removed, rules tightened on existing fields, fields added.
  - **Deprecation policy.** A field is marked deprecated in its doc comment (which `kubectl explain` shows) and in the changelog, and keeps working through every patch of that minor. At the earliest, the next minor removes it. A patch release never removes a field.
  - **Tested upgrades.** The page lists the upgrades a suite tests: from v0.8.1, v0.9.0, v0.10.0, v0.11.0, v0.12.0, v0.12.1 and v0.12.3 straight to the current release.
  - **What a controller rollback does.** The page also notes that there is no CRD conversion, and that no test downgrades kardinal.
  - **The 1.0 criteria.** Each one comes with where it stands today.

  The [roadmap](roadmap.md), the [upgrade guide](upgrade.md) and the [comparison](comparison.md) link to it.
