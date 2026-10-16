---
section: Security
---

- **The UI's opt-in to API server tokens no longer reaches the Bundle API, and neither API accepts a Pod's default ServiceAccount token** (#1930). `tokenReview.acceptAPIServerAudience` (`--tokenreview-accept-apiserver-audience`), which single sign-on needs, made `POST /api/v1/bundles` accept any token for the API server's audience too. That includes the default ServiceAccount token every Pod mounts. The opt-in now applies to the UI API only. The Bundle API has its own, `bundleAPI.acceptAPIServerAudience` (`--bundle-api-tokenreview-accept-apiserver-audience`), off by default. With either opt-in, a ServiceAccount token for the API server's audience is refused (401): a ServiceAccount can mint a token for `kardinal-promoter` ([TokenReview](guides/security.md#option-2-kubernetes-tokens-tokenreview)).
