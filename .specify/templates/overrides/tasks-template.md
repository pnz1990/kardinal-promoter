# Tasks: [FEATURE NAME]

**Input**: `.specify/specs/[NNN-feature-name]/spec.md` + `docs/design/[corresponding design doc].md`
**Prerequisites**: spec.md required
**Feature Branch**: `[NNN-feature-name]`
**Constitution ref**: `.specify/memory/constitution.md`

**Go standards**: Apache 2.0 headers on all new files, `fmt.Errorf("context: %w", err)` for errors, zerolog for logging, table-driven tests with `testify`, `go test -race`.

## Format: `[ID] [P?] [Story] Description — file: path/to/file.go`

- **[P]**: Can run in parallel (independent files, no data dependency)
- Include exact file paths in every task
- Test tasks PRECEDE implementation tasks (TDD)
- Run `/speckit.verify-tasks.run` after marking tasks complete

---

## Phase 1: Setup

**Purpose**: [What this phase establishes. E.g., "Copyright headers, Go module additions, CRD type registration."]
**Checkpoint**: `go build ./...` and `go vet ./...` pass with zero errors.

- [ ] T001 Create `pkg/[package]/[file].go` with Apache 2.0 header and package declaration — file: `pkg/[package]/[file].go`
- [ ] T002 [P] Add types to `pkg/[package]/types.go` — file: `pkg/[package]/types.go`

---

## Phase 2: Tests First (TDD — write before implementation)

**Purpose**: Red phase. Write all tests, verify they fail with the right errors.
**Checkpoint**: `go test ./pkg/[package]/... -run TestXxx` fails with "not implemented" or compile errors.

- [ ] T003 Write unit test for [function]: [what it tests] — file: `pkg/[package]/[file]_test.go`
- [ ] T004 [P] Write unit test for [edge case] — file: `pkg/[package]/[file]_test.go`

---

## Phase 3: Implementation

**Purpose**: Green phase. Implement to make tests pass.
**Checkpoint**: `go test ./pkg/[package]/... -race` passes.

- [ ] T005 Implement [function]: [what it does] — file: `pkg/[package]/[file].go`
- [ ] T006 [P] Implement [function]: [what it does] — file: `pkg/[package]/[file].go`

---

## Phase 4: Integration

**Purpose**: Wire implementation into the controller and validate end-to-end.
**Checkpoint**: `go test ./... -race` passes. Acceptance scenarios from spec validated.

- [ ] T007 Register [component] in controller setup — file: `internal/controller/setup.go`
- [ ] T008 Write integration test using envtest — file: `pkg/[package]/integration_test.go`
