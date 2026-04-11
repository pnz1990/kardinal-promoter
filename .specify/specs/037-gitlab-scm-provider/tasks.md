# Tasks: 037-gitlab-scm-provider

## Phase 1 — Setup

- [ ] T001 Create feature worktree and branch `feat/037-gitlab-scm-provider` — file: git worktree
- [ ] T002 [P] Read existing pkg/scm/github.go and pkg/scm/provider.go — file: pkg/scm/

## Phase 2 — Tests First (TDD)

- [ ] T003 Write TestGitLabProvider_OpenPR (httptest, success + MR already exists) — file: pkg/scm/scm_test.go
- [ ] T004 [P] Write TestGitLabProvider_ClosePR (httptest) — file: pkg/scm/scm_test.go
- [ ] T005 [P] Write TestGitLabProvider_CommentOnPR (httptest) — file: pkg/scm/scm_test.go
- [ ] T006 [P] Write TestGitLabProvider_GetPRStatus (table: opened/merged/closed) — file: pkg/scm/scm_test.go
- [ ] T007 [P] Write TestGitLabProvider_ParseWebhookEvent (valid + invalid token) — file: pkg/scm/scm_test.go
- [ ] T008 [P] Write TestGitLabProvider_AddLabelsToPR (httptest) — file: pkg/scm/scm_test.go
- [ ] T009 Write TestNewProvider (github returns GitHubProvider, gitlab returns GitLabProvider) — file: pkg/scm/scm_test.go
- [ ] T010 Verify tests FAIL (red) before implementation: go test ./pkg/scm/...

## Phase 3 — Implementation

- [ ] T011 Implement pkg/scm/gitlab.go: GitLabProvider struct + NewGitLabProvider + all 6 methods — file: pkg/scm/gitlab.go
- [ ] T012 Implement pkg/scm/factory.go: NewProvider factory function — file: pkg/scm/factory.go
- [ ] T013 Update cmd/kardinal-controller/main.go: add --scm-provider flag, use scm.NewProvider — file: cmd/kardinal-controller/main.go
- [ ] T014 Create docs/scm-providers.md with GitHub and GitLab sections — file: docs/scm-providers.md

## Phase 4 — Validation

- [ ] T015 go test ./pkg/scm/... -race passes (all tests green)
- [ ] T016 go build ./... passes
- [ ] T017 go vet ./... passes
- [ ] T018 /speckit.verify-tasks.run
