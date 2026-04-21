# Spec: issue-969 — Git credential rotation with zero downtime

## Design reference

- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 2: Production stability`
- **Design item**: Git credential rotation with zero downtime (🔲 → ✅)

## Summary

When the Kubernetes Secret containing the SCM token (GitHub PAT, GitLab private
token) is rotated, the controller must reload the new token without a restart.
A controller restart causes a gap in promotions and introduces downtime for
teams that rotate credentials on a schedule.

---

## Zone 1 — Obligations (must be satisfied for PR approval)

### O1: DynamicProvider wraps SCMProvider atomically
- `pkg/scm/dynamic.go` exists
- `DynamicProvider` embeds `atomic.Pointer[SCMProvider]`
- All `SCMProvider` interface methods delegate to `d.current()`
- `Reload(token string) error` creates a new provider and atomically stores it
- `Reload("")` (empty token) is a no-op — does NOT replace the provider
- `DynamicProvider` satisfies the `SCMProvider` interface (verified by `var _ scm.SCMProvider = dp` in tests)

### O2: SecretWatcher polls and reloads
- `pkg/scm/secret_watcher.go` exists
- `SecretWatcher` polls the named Secret at `≤ 60s` interval (implementation uses 30s)
- On token change: calls `DynamicProvider.Reload(token)` and logs the rotation
- On missing Secret or missing key: logs error/warning but does NOT crash or stop the watcher
- Registered as a `manager.Runnable` (implements `Start(ctx context.Context) error`)
- Stops cleanly when `ctx` is cancelled (returns `nil`)

### O3: Wired into main.go with new flags
- Three new flags/env vars:
  - `--scm-token-secret-name` / `KARDINAL_SCM_TOKEN_SECRET_NAME`
  - `--scm-token-secret-namespace` / `KARDINAL_SCM_TOKEN_SECRET_NAMESPACE`
  - `--scm-token-secret-key` / `KARDINAL_SCM_TOKEN_SECRET_KEY`
- When `--scm-token-secret-name` is set: `DynamicProvider` is used and `SecretWatcher` is registered
- When not set: static provider used (existing behaviour, fully backwards compatible)
- Namespace defaults: `flag > KARDINAL_SCM_TOKEN_SECRET_NAMESPACE > POD_NAMESPACE > "kardinal-system"`
- Key defaults to `"token"` when flag is empty

### O4: Helm chart propagates secret reference automatically
- `chart/kardinal-promoter/templates/deployment.yaml`: when `github.secretRef.name` is set,
  inject `KARDINAL_SCM_TOKEN_SECRET_NAME`, `KARDINAL_SCM_TOKEN_SECRET_NAMESPACE`, and
  `KARDINAL_SCM_TOKEN_SECRET_KEY` env vars so zero-downtime rotation is enabled without
  extra chart configuration

### O5: Tests cover concurrent reload and error paths
- `TestDynamicProvider_ConcurrentReload`: race detector passes under concurrent Reload + ParseWebhookEvent
- `TestSecretWatcher_CheckAndReload`: fake client detects two successive token changes
- `TestSecretWatcher_MissingKey`: graceful (no panic) when key absent
- `TestSecretWatcher_MissingSecret`: graceful (no panic) when Secret absent
- `TestDynamicProvider_UnknownProvider`: error propagated from `NewProvider`

### O6: Design doc updated
- `docs/design/15-production-readiness.md`: item `🔲` → `✅` with implementation details

---

## Zone 2 — Constraints (non-negotiable)

- **No restart required**: token change must not require a controller restart
- **Backwards compatible**: existing `--github-token` + `GITHUB_TOKEN` workflow unchanged
- **No business logic in SecretWatcher**: only reads Secret, writes to atomic pointer
- **No CRD status writes from SecretWatcher or DynamicProvider**
- **No mutex in DynamicProvider hot path**: atomic.Pointer only
- **Polling interval ≤ 60s**: teams that rotate on a schedule should not wait > 1 minute
- **No Graph-first violations**: SecretWatcher is infrastructure, not a reconciler

---

## Zone 3 — Out of scope

- TokenReview-based auth for UI API (separate item #975)
- SCM token scope validation at startup (separate item #977)
- Watching Secret for changes via informer events (polling is simpler and adequate)
- Hot-reloading the webhook HMAC secret (webhookSecret is less sensitive; restart is acceptable)
