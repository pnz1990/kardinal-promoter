# Spec: Bitbucket Cloud and Azure DevOps SCM Providers

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future – Lens 1: Kargo parity`
- **Implements**: Bitbucket and Azure DevOps SCM providers (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

O1. `NewProvider("bitbucket", token, apiURL, webhookSecret)` returns a non-nil `SCMProvider`
    and nil error. Before this PR: it returns an error with "bitbucket" in the message.

O2. `NewProvider("azuredevops", token, apiURL, webhookSecret)` returns a non-nil `SCMProvider`
    and nil error. Before this PR: it returns an error with "azuredevops" in the message.

O3. `BitbucketProvider.OpenPR` calls `POST /2.0/repositories/{workspace}/{repo_slug}/pullrequests`
    with `Authorization: Bearer <token>` and returns the PR URL and PR ID on success.
    It is idempotent: if a PR already exists for the head branch (409), returns the existing PR.

O4. `AzureDevOpsProvider.OpenPR` calls `POST /{org}/{project}/_apis/git/repositories/{repo}/pullrequests?api-version=7.1`
    with `Authorization: Basic <base64(:<PAT>)>` and returns the PR URL and PR ID on success.
    It is idempotent: if a PR already exists (TF401179), returns the existing PR.

O5. All `SCMProvider` interface methods are implemented for both providers (OpenPR, ClosePR,
    CommentOnPR, GetPRStatus, GetPRReviewStatus, ParseWebhookEvent, AddLabelsToPR).

O6. `ParseWebhookEvent` for Bitbucket validates the `X-Hub-Signature` HMAC-SHA256 header
    when `WebhookSecret` is non-empty; passes when secret is empty.

O7. `ParseWebhookEvent` for Azure DevOps validates a shared-secret header
    `X-AzureDevOps-Token` using constant-time comparison when `WebhookSecret` is non-empty.

O8. Both providers use `CircuitBreaker` (same as GitHub/GitLab/Forgejo) for all outbound HTTP calls.

O9. Both provider files include the Apache 2.0 copyright header.

O10. Tests exist for `OpenPR`, `ClosePR`, `CommentOnPR`, `GetPRStatus`, `ParseWebhookEvent`
     for both providers using `httptest.NewServer` (no live network calls).

O11. `TestNewProvider_Bitbucket` and `TestNewProvider_AzureDevOps` pass (factory tests).

O12. The existing `TestNewProvider_Unknown` test is updated to use a truly unknown provider
     (e.g., `"badprovider"`) since `"bitbucket"` is now valid.

---

## Zone 2 — Implementer's judgment

- Bitbucket Cloud API v2.0 — use app password with `Authorization: Basic <base64(user:apppassword)>`
  or `Authorization: Bearer <token>`. Since we take a single `token` string, use Bearer for Cloud
  and note in docs that Bitbucket app passwords must be formatted as `user:apppassword` encoded.
  Simplest: use `Authorization: Bearer <token>` and let the caller base64-encode if needed.
  Decision: match GitHub/Forgejo pattern — use `Authorization: Bearer <token>`.

- Bitbucket Webhooks: use `X-Hub-Signature` header with HMAC-SHA256 (same scheme as GitHub).
  Bitbucket's native scheme is actually `X-Hub-Signature` with `sha256=...` prefix.

- Azure DevOps: PAT token authentication uses `Authorization: Basic base64(:<PAT>)`.
  The username part is empty when using PAT (standard ADO pattern).

- Azure DevOps PR labels: ADO uses "work item" tagging, not labels. `AddLabelsToPR` for ADO
  is a no-op that returns nil (platform limitation — ADO does not support PR labels).

- `GetPRReviewStatus` for Bitbucket: check PR reviewers from `GET /pullrequests/{id}` —
  any reviewer with `approved=true` counts.

- `GetPRReviewStatus` for ADO: check `GET /_apis/git/repositories/{repo}/pullrequests/{id}/reviewers`
  — vote >= 10 = approved, vote <= -10 = rejected, return approved=false if any rejection.

---

## Zone 3 — Scoped out

- Bitbucket Server (Data Center) — different API endpoints, out of scope.
- Azure DevOps webhook HMAC signing — ADO uses Basic auth for webhooks, not HMAC. We validate
  a shared secret in a custom header as the best available option.
- OAuth2 flows for either platform — token is provided by the operator via flag/secret.
- Repository creation or branch management — only PR lifecycle operations.
