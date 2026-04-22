# Tasks: issue-984 Bitbucket and Azure DevOps SCM Providers

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-984 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-984 && go test ./pkg/scm/... -count=1 -timeout 60s` — expected: PASS

## Implementation
- [AI] Create `pkg/scm/bitbucket.go` with BitbucketProvider implementing SCMProvider
- [CMD] `cd ../kardinal-promoter.issue-984 && go build ./pkg/scm/...` — expected: 0 exit
- [AI] Create `pkg/scm/azuredevops.go` with AzureDevOpsProvider implementing SCMProvider
- [CMD] `cd ../kardinal-promoter.issue-984 && go build ./pkg/scm/...` — expected: 0 exit
- [AI] Update `pkg/scm/factory.go` to add "bitbucket" and "azuredevops" cases
- [CMD] `cd ../kardinal-promoter.issue-984 && go build ./...` — expected: 0 exit
- [AI] Add tests for both providers in `pkg/scm/scm_test.go` (append after existing tests)
- [CMD] `cd ../kardinal-promoter.issue-984 && go test ./pkg/scm/... -race -count=1 -timeout 60s` — expected: PASS
- [AI] Update `docs/design/15-production-readiness.md` to move item from 🔲 to ✅
- [AI] Update `docs/scm-providers.md` (or create it) with Bitbucket/Azure DevOps config docs

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-984 && go test ./... -race -count=1 -timeout 120s` — expected: PASS
- [CMD] `cd ../kardinal-promoter.issue-984 && go vet ./...` — expected: 0 exit
