# Spec: UI API Authentication (Issue #909)

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future — UI authentication gaps`
- **Implements**: UI API authentication — `ui_api.go` has no auth middleware (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — When `--ui-auth-token` flag is set (non-empty), every request to `/api/v1/ui/*` that
does NOT include an `Authorization: Bearer <token>` header matching the configured token MUST
return HTTP 401 with body `"unauthorized\n"`.

**O2** — When `--ui-auth-token` flag is set, a request with a correct `Authorization: Bearer <token>`
header MUST proceed to the handler and return the normal response (200/201/etc.).

**O3** — When `--ui-auth-token` flag is NOT set (empty string), all `/api/v1/ui/*` routes MUST
serve without authentication (backwards-compatible default — existing deployments without the flag
are unchanged).

**O4** — Static UI assets at `/ui/*` (the embedded React app) MUST NOT be gated by the auth
middleware — they contain no sensitive data and must load without credentials.

**O5** — The middleware MUST set `Www-Authenticate: Bearer realm="kardinal-ui"` on 401 responses.

**O6** — The `--ui-auth-token` flag MUST be readable from the `KARDINAL_UI_TOKEN` environment
variable as fallback (consistent with `--bundle-api-token` / `KARDINAL_BUNDLE_TOKEN` pattern).

**O7** — Unit tests cover: unauthenticated request → 401 when token configured; correct
Bearer token → 200; no token configured → 200 (open mode).

---

## Zone 2 — Implementer's judgment

- Auth is implemented as an `http.Handler` middleware wrapping the `uiMux` in `main.go`
  (not per-route logic inside `ui_api.go`) — the middleware wraps the mux after
  `RegisterRoutes` is called but before `/ui/` static assets are registered.
  Actually: per O4, static assets must bypass auth. So the middleware is applied only to
  the API routes, not to the whole mux. Implement as a wrapper on the routes registered
  by `uiAPI.RegisterRoutes(...)` using a sub-mux, or as an inline middleware that
  skips `/ui/` paths.
- Token comparison uses `subtle.ConstantTimeCompare` to prevent timing attacks.
- No rate limiting required for the UI auth (the bundle API has rate limiting because it is
  exposed to CI systems; the UI is accessed interactively by cluster operators).

---

## Zone 3 — Scoped out

- Kubernetes ServiceAccount TokenReview (k8s OIDC) — a future enhancement.
- TLS (tracked separately in issue #911).
- CORS (tracked separately in issue #912).
- Per-route token granularity — one token for all UI API routes.
