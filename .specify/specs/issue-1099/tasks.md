# Tasks: issue-1099 — Kubernetes Events from reconcilers

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go build ./...` — expected: 0 exit

## Implementation

- [AI] Add `record.EventRecorder` field to `pkg/reconciler/bundle/reconciler.go` Reconciler struct; emit events on Bundle phase transitions (O1)
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go build ./pkg/reconciler/bundle/...` — expected: 0 exit
- [AI] Add `record.EventRecorder` field to `pkg/reconciler/promotionstep/reconciler.go` Reconciler struct; emit events on PromotionStep state transitions (O2)
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go build ./pkg/reconciler/promotionstep/...` — expected: 0 exit
- [AI] Add `record.EventRecorder` field to `pkg/reconciler/policygate/reconciler.go` Reconciler struct; emit event when gate blocks (O3)
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go build ./pkg/reconciler/policygate/...` — expected: 0 exit
- [AI] Wire EventRecorder via `mgr.GetEventRecorderFor("kardinal-controller")` in `cmd/kardinal-controller/main.go` for each reconciler (O5)
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go build ./...` — expected: 0 exit
- [AI] Write tests using `record.NewFakeRecorder` in bundle, promotionstep, policygate test files (O7)

## Post-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go build ./...` — expected: 0 exit
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go vet ./...` — expected: 0 exit
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1099 && go test ./pkg/reconciler/bundle/... ./pkg/reconciler/promotionstep/... ./pkg/reconciler/policygate/... -race -count=1 -timeout 60s` — expected: all pass
