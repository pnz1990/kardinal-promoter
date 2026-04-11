# Spec: 037-gitlab-scm-provider

**Feature branch**: `feat/037-gitlab-scm-provider`
**Item**: `docs/aide/items/037-gitlab-scm-provider.md`
**GitHub Issue**: #120

---

## Background

`pkg/scm/` has `SCMProvider` interface and a `GitHubProvider` implementation.
`cmd/kardinal-controller/main.go` hardcodes `scm.NewGitHubProvider(...)`.
Workshop 2 requires GitLab as the SCM. When `pipeline.spec.git.provider == "gitlab"`,
the controller must use the GitLab REST API v4 instead of GitHub.

---

## User Scenarios

### Scenario 1 — GitLab MR creation

**Given** a Pipeline with `spec.git.provider: gitlab`  
**When** a Bundle promotion reaches the open-pr step  
**Then** a GitLab MR is opened via `POST /projects/:id/merge_requests`  
**And** the MR URL is stored in PromotionStep status

### Scenario 2 — GitLab MR merge detection

**Given** a PromotionStep waiting for merge on a GitLab MR  
**When** the MR is merged in GitLab  
**Then** `GetPRStatus` returns `merged=true`

### Scenario 3 — Webhook validation

**Given** a GitLab webhook with `X-Gitlab-Token: secret`  
**When** `ParseWebhookEvent` is called with the matching secret  
**Then** the event is parsed; mismatched tokens return an error

### Scenario 4 — Provider dispatch

**Given** a controller started with `--scm-provider gitlab`  
**When** the controller reconciles a Bundle  
**Then** all SCM operations use `GitLabProvider`

---

## Functional Requirements

| ID | Requirement |
|---|---|
| FR-001 | `GitLabProvider` MUST implement all 5 `SCMProvider` methods: `OpenPR`, `ClosePR`, `CommentOnPR`, `GetPRStatus`, `ParseWebhookEvent`, `AddLabelsToPR` |
| FR-002 | `OpenPR` MUST call `POST /api/v4/projects/:encoded_repo/merge_requests` |
| FR-003 | `ClosePR` MUST call `PUT /api/v4/projects/:encoded_repo/merge_requests/:iid` with `state_event=close` |
| FR-004 | `CommentOnPR` MUST call `POST /api/v4/projects/:encoded_repo/merge_requests/:iid/notes` |
| FR-005 | `GetPRStatus` MUST call `GET /api/v4/projects/:encoded_repo/merge_requests/:iid`; return `merged=(state=="merged")`, `open=(state=="opened")` |
| FR-006 | `ParseWebhookEvent` MUST validate `X-Gitlab-Token` header by constant-time comparison |
| FR-007 | `AddLabelsToPR` MUST call `PUT /api/v4/projects/:encoded_repo/merge_requests/:iid` with `labels` param |
| FR-008 | `NewProvider(type, token, apiURL, webhookSecret)` factory MUST return `GitLabProvider` when `type == "gitlab"`, `GitHubProvider` otherwise |
| FR-009 | `cmd/kardinal-controller/main.go` MUST accept `--scm-provider` flag (default `"github"`) and use `NewProvider` |
| FR-010 | `OpenPR` MUST be idempotent: if an MR already exists for the source branch, return its URL+IID without error |
| FR-011 | `docs/scm-providers.md` MUST document GitLab configuration, required token scopes, and webhook setup |

---

## Go Package Structure

```
pkg/scm/
  gitlab.go          # GitLabProvider struct + all 6 SCMProvider methods
  factory.go         # NewProvider(type, token, apiURL, webhookSecret) SCMProvider
  scm_test.go        # existing + new table-driven tests for GitLabProvider
```

---

## Success Criteria

| ID | Criterion |
|---|---|
| SC-001 | `go test ./pkg/scm/... -race -count=1` passes with ≥ 8 new test cases for GitLab |
| SC-002 | `go vet ./...` passes with no errors |
| SC-003 | `go build ./...` passes |
| SC-004 | `GitLabProvider` passes all SCMProvider interface methods with httptest servers |
| SC-005 | `NewProvider("gitlab", ...)` returns a `*GitLabProvider` |
| SC-006 | `NewProvider("github", ...)` returns a `*GitHubProvider` |
| SC-007 | Webhook with wrong token returns an error |
| SC-008 | `docs/scm-providers.md` exists with GitLab and GitHub sections |
