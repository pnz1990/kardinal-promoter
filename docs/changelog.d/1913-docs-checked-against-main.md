---
section: Documentation
---

- **Docs checked against main** (#1913) — every published page, README and CONTRIBUTING was audited against the code, and about 100 wrong or stale claims were fixed. They include RBAC rules, admission policy lists, `kardinal why` samples, fleet cells, UI routes and keys, metric labels, PR titles for chart versions, and the Bundle API with TokenReview. Checks now keep the docs true: every documented `kardinal` command and flag, fenced or inline, must parse with the CLI; every kardinal.io object in a YAML block must have only fields its CRD has; and every inline `spec.*` or `status.*` path must exist in a kardinal CRD. They run in `make hygiene` and in CI.
