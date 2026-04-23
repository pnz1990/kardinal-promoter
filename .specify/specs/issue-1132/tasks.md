# Tasks: issue-1132 — Fix PDCA S1: readyz probe gates on cache sync

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1132 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1132 && go test ./pkg/reconciler/bundle/... -race -count=1 -timeout 60s` — expected: PASS

## Implementation
- [AI] Replace `healthz.Ping` in the `AddReadyzCheck` call in `cmd/kardinal-controller/main.go` with a cache-sync-gated checker
- [CMD] `cd ../kardinal-promoter.issue-1132 && go build ./cmd/kardinal-controller/ 2>&1` — expected: 0 exit
- [AI] Increase S1 wait from 20×15s to 30×15s in `.github/workflows/pdca.yml`
- [CMD] `cd ../kardinal-promoter.issue-1132 && grep "seq 1 30" .github/workflows/pdca.yml | wc -l` — expected: 1

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1132 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1132 && go test ./... -race -count=1 -timeout 120s` — expected: PASS
- [CMD] `cd ../kardinal-promoter.issue-1132 && go vet ./...` — expected: 0 exit
