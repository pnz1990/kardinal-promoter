# Spec: issue-977 — SCM token scopes validated at startup

## Design reference

- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 4: Security posture`
- **Design item**: SCM token scopes are not validated at startup (🔲 → ✅)

## Summary

Add a startup preflight check that calls the SCM provider's token introspection
endpoint and logs a warning if required scopes are missing. A token with only
`read:repo` scope silently fails when the controller tries to open a PR (403
surfaces hours later in a reconcile log). Surface this in minutes at startup.

---

## Zone 1 — Obligations (must be satisfied for PR approval)

### O1: pkg/scm/token_validator.go exists
- `ValidateGitHubTokenScopes(ctx, token, apiURL) ([]TokenScopeWarning, error)`
- `ValidateGitLabTokenScopes(ctx, token, apiURL) ([]TokenScopeWarning, error)`
- `ValidateForgejoTokenScopes(ctx, token, apiURL) ([]TokenScopeWarning, error)`
- `TokenScopeWarning` struct with `MissingScope` and `Consequence` fields and `String()` method

### O2: GitHub scope validation
- Calls `GET /user` (token introspection endpoint)
- Empty token → warning without HTTP call
- 401 response → "token rejected" warning
- `X-OAuth-Scopes` absent (fine-grained PAT) → no warnings (cannot inspect)
- `X-OAuth-Scopes` present but neither `repo` nor `public_repo` → warning for missing `repo` scope
- `X-OAuth-Scopes` contains `repo` OR `public_repo` → no warnings
- 5xx → error returned (caller logs at debug level, non-fatal)

### O3: GitLab scope validation
- Calls `/api/v4/personal_access_tokens/self`
- Empty token → warning
- 401 → warning
- 404 → no warning (OAuth token — cannot inspect)
- Response without `"api"` → warning for missing `api` scope
- Response with `"api"` → no warnings

### O4: Forgejo scope validation
- Calls `/api/v1/user`
- Empty token → warning
- 401 → warning
- 200 → no warnings (cannot inspect Forgejo token scopes from this endpoint)

### O5: Wired into main.go as non-fatal startup check
- Called after provider creation, before `mgr.Start()`
- Check is skipped when `scmTokenSecretName != ""` (DynamicProvider path — initial token may be empty)
- Check is skipped when `githubToken == ""` (nothing to validate)
- Network errors logged at `Debug` level (non-fatal)
- Scope warnings logged at `Warn` level with `missing_scope` and `consequence` fields
- Controller continues to start regardless of warnings

### O6: Design doc updated
- `docs/design/15-production-readiness.md`: item `🔲` → `✅`

### O7: Tests cover all paths
- Empty token, 401, missing scope, valid scope, fine-grained PAT, server error (GitHub)
- Empty token, missing api scope, valid api scope (GitLab)
- Unauthorized, valid token (Forgejo)
- `TokenScopeWarning.String()` output format

---

## Zone 2 — Constraints

- **Non-fatal**: warnings never prevent controller from starting
- **Not in reconciler hot path**: check runs once at startup only
- **Graph-purity**: no CRD status writes, no business logic, no reconciler involvement
- **Context timeout**: the startup check uses `context.WithTimeout(15s)` to avoid hanging

---

## Zone 3 — Out of scope

- Fine-grained PAT scope inspection (GitHub API does not expose this from `/user`)
- GitHub App token scope inspection (different auth mechanism)
- Periodic re-checking of scopes after token rotation (rotation is handled by SecretWatcher)
- Hard-failing the controller on missing scopes (warning only, per design doc)
