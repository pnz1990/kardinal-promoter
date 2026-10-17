---
section: Upgrade
---

- **The Bundle CRD refuses a `provenance.ciRunURL` with user info or a credential parameter** (#2158). A CI job, script or GitOps repository that creates Bundles with kubectl and puts a token in the CI run URL (`?token=`, `access_token`, `sig`, ...) gets an error after the CRDs are applied. Remove the token from the URL. A Bundle already stored with such a URL stays writable while the URL is unchanged, and its receipts redact it ([Provenance](ci-integration.md#provenance))
