# Tasks: issue-975 TokenReview auth

## Tasks
- [CMD] Write spec.md (done above)
- [AI] Create `pkg/uiauth/tokenreview.go` with `TokenReviewer` interface and `KubeTokenReviewer` implementation
- [AI] Add `--ui-tokenreview-auth` flag in `cmd/kardinal-controller/main.go`
- [AI] Wire TokenReview middleware in UI server setup
- [AI] Write unit tests in `cmd/kardinal-controller/ui_tokenreview_test.go`
- [AI] Update `docs/design/15-production-readiness.md` (🔲 → ✅)
- [AI] Update `docs/design/06-kardinal-ui.md` (add Present entry)
- [CMD] go build ./... && go test ./... && go vet ./...
