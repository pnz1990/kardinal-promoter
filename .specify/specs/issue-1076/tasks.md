# Tasks: issue-1076 — GoNativeType nil CEL value sentinel

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1076 && go build ./...` — expected: 0 exit

## Implementation
- [AI] Add `ErrNilCELValue` sentinel error to `pkg/cel/conversion/conversion.go`
- [AI] Change `if v == nil { return nil, nil }` to `if v == nil { return nil, ErrNilCELValue }`
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1076 && go build ./...` — expected: 0 exit
- [AI] Write table-driven tests in `pkg/cel/conversion/conversion_test.go` covering O1, O3, O4
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1076 && go test ./pkg/cel/conversion/... -race -count=1` — expected: all pass

## Post-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1076 && go test ./... -race -count=1 -timeout 120s` — expected: all pass
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1076 && go vet ./...` — expected: 0 exit
