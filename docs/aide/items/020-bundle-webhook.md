# Item 020: GitHub Action + Bundle Webhook Endpoint

> **Queue**: queue-009
> **Branch**: `020-bundle-webhook`
> **Depends on**: 017 (kardinal init, merged), 012 (SCM provider + steps engine, merged)
> **Dependency mode**: merged
> **Contributes to**: J1 (CI integration — Bundle creation from GitHub Actions)
> **Priority**: HIGH — completes J1 end-to-end (CI triggers promotion)

---

## Goal

Deliver the GitHub Actions workflow that creates Bundles from CI and the
`/api/v1/bundles` webhook endpoint with HMAC authentication and rate limiting.

---

## Deliverables

### 1. Bundle webhook endpoint

In `cmd/kardinal-controller/bundle_api.go`:
- `POST /api/v1/bundles` endpoint:
  - Validates `Authorization: Bearer <token>` header + HMAC signature
  - Parses JSON body: `{pipeline, type, images: [{repository, tag}], provenance: {commitSHA, ciRunURL, author}}`
  - Creates a `Bundle` CRD in the target namespace
  - Returns `{"name": "<bundle-name>", "namespace": "<ns>"}` JSON
  - Rate limited: 60 requests/minute per token (token bucket, in-memory per token)
  - Request size limit: 1 MB
- Add `--bundle-api-token` flag (or env `KARDINAL_BUNDLE_TOKEN`) — HMAC key for Bundle API
- Mount at `POST /api/v1/bundles` on the webhook server (port 8083)

### 2. GitHub Action

In `.github/actions/create-bundle/`:
- `action.yml` with inputs: `pipeline`, `type`, `images`, `namespace`, `kardinal-url`
- `entrypoint.sh` (bash): calls `POST /api/v1/bundles` with Bearer token from `KARDINAL_TOKEN` env
- Auto-populates provenance from `GITHUB_SHA`, `GITHUB_RUN_URL`, `GITHUB_ACTOR`
- Outputs: `bundle-name`, `bundle-status-url`

### 3. Documentation

Update `docs/ci-integration.md`:
- Usage example for the GitHub Action
- Token setup instructions (create Kubernetes Secret, pass as env)
- Rate limit and HMAC signature explanation

### 4. Unit tests

- `TestBundleAPI_CreateBundle`: POST /api/v1/bundles creates a Bundle CRD
- `TestBundleAPI_RejectsInvalidToken`: POST without valid Bearer token returns 401
- `TestBundleAPI_RateLimits`: >60 requests/minute from same token returns 429

---

## Acceptance Criteria

- [ ] `POST /api/v1/bundles` creates a Bundle CRD visible via `kardinal get bundles`
- [ ] Requests without valid Bearer token return 401
- [ ] >60 requests/minute from same token return 429
- [ ] GitHub Action YAML is valid (`yamllint`)
- [ ] `docs/ci-integration.md` updated with working example
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new Go files
- [ ] No banned filenames
