# Item: 037-gitlab-scm-provider

> dependency_mode: merged
> depends_on: 012-scm-and-steps-engine

## Summary

Implement `GitLabProvider` in `pkg/scm/` — a full `SCMProvider` implementation against the GitLab REST API v4. Add provider dispatch in `pkg/scm/` so callers can call `scm.NewProvider(providerType, token, apiURL, webhookSecret)` and get back either a `GitHubProvider` or `GitLabProvider`. Update `cmd/kardinal-controller/main.go` to dispatch based on `Pipeline.spec.git.provider`.

Workshop 2 (Fleet Management on EKS) uses GitLab. Without this, the controller always uses GitHub even when `pipeline.spec.git.provider == "gitlab"`.

## GitHub Issue

#120 — feat(scm): implement GitLab SCM provider

## Acceptance Criteria

- [ ] `GitLabProvider` struct implementing `SCMProvider` interface (all 5 methods)
- [ ] `NewProvider(providerType, token, apiURL, webhookSecret)` factory in `pkg/scm/factory.go` dispatches to GitHub or GitLab
- [ ] MR creation: `POST /projects/:id/merge_requests`
- [ ] MR merge detection: `GET /projects/:id/merge_requests/:iid` → `state == "merged"`
- [ ] MR comment: `POST /projects/:id/merge_requests/:iid/notes`
- [ ] MR label: `PUT /projects/:id/merge_requests/:iid` with `labels` param
- [ ] Webhook validation: `X-Gitlab-Token` header comparison
- [ ] `cmd/kardinal-controller/main.go` uses `NewProvider` + reads `--scm-provider` flag (default: `github`)
- [ ] `docs/scm-providers.md` created with GitLab configuration instructions
- [ ] `go test ./pkg/scm/... -race` passes with table-driven tests for all GitLab methods

## Files to modify/create

- `pkg/scm/gitlab.go` (new — GitLabProvider implementation)
- `pkg/scm/factory.go` (new — NewProvider factory)
- `pkg/scm/scm_test.go` (add GitLab tests)
- `cmd/kardinal-controller/main.go` (use NewProvider, add --scm-provider flag)
- `docs/scm-providers.md` (new — provider configuration docs)

## Size: L
