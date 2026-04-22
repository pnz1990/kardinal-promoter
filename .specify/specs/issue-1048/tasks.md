# Tasks: issue-1048 — Surface dependsOn validation errors in CLI output

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1048 && go build ./...` — expected: exit 0
- [CMD] `cd ../kardinal-promoter.issue-1048 && go test ./cmd/kardinal/cmd/... -count=1 -timeout 60s` — expected: PASS

## Implementation
- [AI] Add `FormatBundleErrors(w io.Writer, bundles []v1alpha1.Bundle, pipelineFilter []string) error` to `format.go` — prints ERROR lines for Failed bundles, noop when none
- [CMD] `cd ../kardinal-promoter.issue-1048 && go build ./cmd/kardinal/...` — expected: exit 0
- [AI] Update `getPipelinesOnce` in `get_pipelines.go` to fetch Bundles (non-fatal on error) and call `FormatBundleErrors` after the table
- [CMD] `cd ../kardinal-promoter.issue-1048 && go build ./cmd/kardinal/...` — expected: exit 0
- [AI] Write tests in `format_test.go` for `FormatBundleErrors`: (a) Failed bundle shows ERROR line, (b) no Failed bundles shows nothing, (c) CircularDependency condition is shown
- [AI] Write integration test in `get_pipelines_test.go`: `TestGetPipelinesOnce_FailedBundle_ShowsError`
- [CMD] `cd ../kardinal-promoter.issue-1048 && go test ./cmd/kardinal/cmd/... -count=1 -timeout 60s -run TestFormatBundleErrors` — expected: PASS
- [AI] Update design doc 39 to move 🔲 Future item to ✅ Present
- [AI] Update docs/cli-reference.md to document the error output format (if file exists)

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1048 && go build ./...` — expected: exit 0
- [CMD] `cd ../kardinal-promoter.issue-1048 && go test ./cmd/kardinal/cmd/... -race -count=1 -timeout 120s` — expected: PASS
- [CMD] `cd ../kardinal-promoter.issue-1048 && go vet ./...` — expected: exit 0
