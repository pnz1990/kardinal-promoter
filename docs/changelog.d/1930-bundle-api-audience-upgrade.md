---
section: Upgrade
---

- **TokenReview: the API server audience opt-in is per API** (#1930). Check this if you set `tokenReview.acceptAPIServerAudience=true`.
  - **Bundle API.** If CI posts to `/api/v1/bundles` with a kubeconfig token or another token for the API server's audience, it now gets 401. Mint a token for kardinal instead (`kubectl create token <sa> --audience kardinal-promoter`, or a projected token with `audience: kardinal-promoter`). If you must keep a person's API server tokens there, set `bundleAPI.acceptAPIServerAudience=true`.
  - **UI API.** A ServiceAccount token without `--audience kardinal-promoter` now gets 401 there, even with the opt-in. Single sign-on users (OIDC ID tokens) are not affected.
  - **Startup.** With `tokenReview.audiences` empty, each API that uses TokenReview needs its own opt-in. Otherwise the chart (and the controller) refuses to start and names that API's setting.
