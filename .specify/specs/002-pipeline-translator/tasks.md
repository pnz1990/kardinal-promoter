# Tasks: Pipeline-to-Graph Translator

**Input**: `.specify/specs/002-pipeline-translator/spec.md` + `docs/design/02-pipeline-to-graph-translator.md`
**Feature Branch**: `002-pipeline-translator`
**Test command**: `go test ./pkg/translator/... -race`

---

## Phase 1: Setup

**Checkpoint**: `go build ./pkg/translator/...` succeeds.

- [ ] T001 Create `pkg/translator/` with package skeleton files and Apache 2.0 headers — files: `pkg/translator/translator.go`, `environment.go`, `gates.go`, `skip.go`, `graph.go`

---

## Phase 2: Tests First

**Checkpoint**: Tests compile but fail.

- [ ] T002 Write `TestLinearPipeline`: 3-env pipeline → 3 PromotionStep nodes in sequential order — file: `pkg/translator/translator_test.go`
- [ ] T003 [P] Write `TestFanOut`: two envs with `dependsOn: [staging]` → parallel nodes — file: `pkg/translator/translator_test.go`
- [ ] T004 [P] Write `TestIntentTarget`: `target: staging` excludes prod node — file: `pkg/translator/translator_test.go`
- [ ] T005 [P] Write `TestPolicyGateInjection`: 2 org gates on prod → 2 PolicyGate nodes between staging and prod — file: `pkg/translator/translator_test.go`
- [ ] T006 [P] Write `TestSkipPermissionDenied`: `intent.skip: [staging]` with org gate and no SkipPermission → returns SkipDenied error — file: `pkg/translator/translator_test.go`
- [ ] T007 [P] Write `TestSkipPermissionGranted`: hotfix Bundle + SkipPermission gate → staging excluded — file: `pkg/translator/translator_test.go`
- [ ] T008 [P] Write `TestCircularDependency`: `dependsOn` creates cycle → returns error — file: `pkg/translator/translator_test.go`

---

## Phase 3: Implementation

**Checkpoint**: `go test ./pkg/translator/... -race` passes.

- [ ] T009 Implement `environment.go`: build dependency map, validate no cycles, validate no unknown env references — file: `pkg/translator/environment.go`
- [ ] T010 [P] Implement `gates.go`: scan policy namespaces + Pipeline namespace, match by `applies-to` label, collect per environment — file: `pkg/translator/gates.go`
- [ ] T011 [P] Implement `skip.go`: validate skip permissions synchronously — file: `pkg/translator/skip.go`
- [ ] T012 Implement `graph.go`: assemble Graph spec with PromotionStep and PolicyGate nodes, `upstreamVerified`/`requiredGates` CEL references for edge inference — file: `pkg/translator/graph.go`
- [ ] T013 Implement `translator.go`: `Translate()` entrypoint calling all phases in order — file: `pkg/translator/translator.go`

---

## Phase 4: Validation

- [ ] T014 Verify `go test ./pkg/translator/... -race` passes
- [ ] T015 [P] Verify `go vet ./pkg/translator/...` passes
- [ ] T016 [P] Apache 2.0 headers on all files
- [ ] T017 Run `/speckit.verify-tasks.run`
