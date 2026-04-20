# Spec: feat(controller): CORS lockdown for UI API (issue #912)

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future — UI authentication gaps`
- **Implements**: CORS lockdown for UI API (🔲 → ✅)

## Zone 1 — Obligations (falsifiable)

1. **O1 — Same-origin default**: When no `--cors-allowed-origins` flag is set (or env `KARDINAL_CORS_ORIGINS`
   is empty), the server responds to cross-origin requests to `/api/v1/ui/*` with HTTP 403.
   A request with `Origin: https://evil.com` to `GET /api/v1/ui/pipelines` returns 403.

2. **O2 — Explicit allow-list**: When `--cors-allowed-origins=https://allowed.example.com` is set,
   a preflight `OPTIONS` request from `Origin: https://allowed.example.com` to `/api/v1/ui/pipelines`
   returns HTTP 200 with `Access-Control-Allow-Origin: https://allowed.example.com`.

3. **O3 — Wildcard opt-out**: When `--cors-allowed-origins=*` is set, all origins are allowed
   (same as no CORS restriction). This is the escape hatch for development environments.

4. **O4 — Static assets unguarded**: CORS headers are only applied to `/api/v1/ui/*` routes.
   Static `/ui/*` assets are not subject to CORS enforcement.

5. **O5 — No CORS on non-UI routes**: `/webhook/*` and `/api/v1/bundles` are not affected.

6. **O6 — Flag readable from env**: `--cors-allowed-origins` has an environment variable fallback
   `KARDINAL_CORS_ORIGINS`. A comma-separated list of origins is supported.

7. **O7 — Backwards compatible default**: When the flag is not set, existing behaviour is unchanged
   except for the addition of CORS enforcement (same-origin only). No authentication breakage.

8. **O8 — Test coverage**: Unit tests in `cmd/kardinal-controller/` cover:
   - O1: cross-origin rejected when no flag set
   - O2: cross-origin allowed when origin in allow-list
   - O3: wildcard allows all
   - O4: static asset not CORS-guarded

## Zone 2 — Implementer's judgment

- Whether to implement CORS as a middleware wrapper or inside each handler (middleware preferred).
- Whether to write CORS headers on all responses or only on OPTIONS preflight (both is correct per spec).
- Exact HTTP headers to include in CORS response (`Access-Control-Allow-Methods`, `Access-Control-Allow-Headers`).
- Whether to parse the flag as comma-separated or repeated flag values (comma-separated is simpler).

## Zone 3 — Scoped out

- CORS for the webhook server (different threat model — webhooks come from GitHub/GitLab, not browsers).
- `Access-Control-Max-Age` tuning.
- TokenReview-based authentication (separate Future item in design doc 15).
- Changing default to open (CORS-open default is a breaking change if ships to prod teams).
