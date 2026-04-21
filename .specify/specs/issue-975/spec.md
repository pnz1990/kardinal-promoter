# Spec: Kubernetes TokenReview-based auth for UI API

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `Lens 4: Security posture — what a Series B security review would flag`
- **Implements**: No Kubernetes TokenReview-based auth for UI API (🔲 → ✅)
- **Also references**: `docs/design/06-kardinal-ui.md § UI authentication gaps`

---

## Zone 1 — Obligations (falsifiable, must satisfy)

**O1**: A new flag `--ui-tokenreview-auth` (env: `KARDINAL_UI_TOKENREVIEW_AUTH`, bool, default `false`) is added to the controller. When set to `true`, the UI API server uses Kubernetes TokenReview for authentication instead of (or as a fallback to) the static Bearer token.

**O2**: When `--ui-tokenreview-auth=true` and `--ui-auth-token` is NOT set, requests to `/api/v1/ui/*` that lack a `Authorization: Bearer <token>` header are rejected with HTTP 401.

**O3**: When `--ui-tokenreview-auth=true` and a Bearer token IS provided, the server calls `authenticationv1.TokenReview` against the Kubernetes API server. If the response's `Status.Authenticated` is `false`, the request is rejected with HTTP 401.

**O4**: When `--ui-auth-token` (static token) IS set, it takes precedence over TokenReview. The static token middleware is applied first; TokenReview is not called when a static token is configured.

**O5**: Static `/ui/*` assets bypass both auth mechanisms (unchanged from PR #924 behavior).

**O6**: When `--ui-tokenreview-auth=true` but the TokenReview API call fails (e.g., network error or missing RBAC), the server returns HTTP 503 with body "auth unavailable" rather than 200 (fail-closed).

**O7**: The TokenReview reviewer is an interface (`TokenReviewer`) injected as a dependency, allowing unit tests to mock the Kubernetes API call.

**O8**: On controller startup, a `--ui-tokenreview-auth=true` flag logs an info-level message: `"UI API TokenReview authentication enabled"`.

**O9**: Tests cover: (a) unauthenticated → 401, (b) valid token → 200, (c) invalid token (TokenReview returns Authenticated=false) → 401, (d) TokenReview API error → 503, (e) static token takes precedence over TokenReview.

---

## Zone 2 — Implementer's judgment

- Token extraction: standard `strings.TrimPrefix(authHeader, "Bearer ")` — same as existing static auth
- `TokenReviewer` interface with a `Review(ctx, token) (*authv1.TokenReviewStatus, error)` method
- Concrete implementation uses `kubernetes.NewForConfig(restConfig).AuthenticationV1().TokenReviews().Create(...)`
- Timeout for TokenReview call: 5 seconds (O6 covers failure)
- No namespace scoping at this tier — TokenReview validates the token identity only, not authorization. Full RBAC scoping is a follow-up Future item (multi-tenant)
- The `--ui-tokenreview-auth` flag coexists with `--ui-auth-token`; priority order documented in code comment

---

## Zone 3 — Scoped out

- RBAC namespace scoping (see `--namespace-scoped controller mode` Future item)
- Token caching / TTL-based short-circuit (performance optimization; not needed for Phase 1 poll-based UI)
- Impersonation support
- OIDC federation (different feature, different design doc)
