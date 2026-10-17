---
section: Upgrade
---

- **The Bundle CRD refuses a `provenance.ciRunURL` with user info, a credential parameter or a percent-encoded parameter name** (#2158). A credential parameter has a name ending in `token`, `secret`, `key`, `signature`, `password`, `session`, `ticket`, `bearer`, ... (not harmless names such as `monkey`), or is `sig`, `auth`, `authorization`, `jwt`, `code` or `sid`. A CI job, script or GitOps repository that creates Bundles with kubectl and puts such a parameter in the CI run URL gets an error once the CRDs are applied, so remove it from the URL. A Bundle already stored with such a URL stays writable while the URL is unchanged, and its receipts redact it ([Provenance](ci-integration.md#provenance))
