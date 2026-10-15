---
section: Development
---

- **Single sign-on has a live suite** (#1986) — the new `sso` suite follows the security guide's "Option 3: single sign-on through an OIDC proxy" on kind, on Kubernetes 1.35 and 1.37: the API server trusts an in-cluster Dex through a structured `AuthenticationConfiguration`, oauth2-proxy runs with the page's flags in front of the UI, and kardinal is upgraded with the page's values. A user in the gate's group signs in and approves through the proxy and is recorded as `oidc:<user>` with the `oidc:` groups; a viewer is refused with `403`; with no session the UI redirects to the login and the UI API answers `401`. The guide now lists `--http-address=0.0.0.0:4180` (oauth2-proxy's default listens on loopback only) and says to ask for the groups scope where the provider needs one.
