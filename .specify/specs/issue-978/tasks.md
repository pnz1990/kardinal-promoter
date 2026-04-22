# Tasks: issue-978 — helm install to first Bundle in under 10 minutes

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-978 && go build ./...` — expected: 0 exit

## Implementation
- [AI] Add `demo.enabled`, `demo.image`, `demo.secretRef.name` to `chart/kardinal-promoter/values.yaml`
- [CMD] `grep -c "demo:" ../kardinal-promoter.issue-978/chart/kardinal-promoter/values.yaml` — expected: ≥1
- [AI] Create `chart/kardinal-promoter/templates/demo.yaml` with Pipeline CR guarded by `{{- if .Values.demo.enabled }}`
- [CMD] `ls ../kardinal-promoter.issue-978/chart/kardinal-promoter/templates/demo.yaml` — expected: file exists
- [AI] Add "Fast Start (< 10 minutes)" section at top of `docs/quickstart.md` with 3-command path
- [CMD] `grep -c "Fast Start" ../kardinal-promoter.issue-978/docs/quickstart.md` — expected: ≥1

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-978 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-978 && go vet ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-978 && go test ./cmd/kardinal/... -race -count=1 -timeout 60s` — expected: all pass
