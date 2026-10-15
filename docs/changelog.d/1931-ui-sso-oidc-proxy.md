---
section: Documentation
---

- **Single sign-on for the UI** (#1931, [guide](guides/security.md#option-3-single-sign-on-through-an-oidc-proxy)) — a recipe for per-user sign-in through an OIDC proxy (oauth2-proxy or similar) and the API server's OIDC authenticator: the proxy forwards each user's ID token, kardinal reviews it with `ui.auth.tokenReview` and `tokenReview.acceptAPIServerAudience`, RBAC applies per person, and approvals from the UI record who approved.
