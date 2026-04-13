# Tasks: [FEATURE NAME]

**Input**: `.specify/specs/[NNN-feature-name]/spec.md`
**Feature Branch**: `[NNN-feature-name]`
**Test command**: `[PROJECT.test_command]`
**Lint command**: `[PROJECT.lint_command]`

**Standards**: Copyright header on all new source files. No banned filenames (see AGENTS.md).
Idempotent handlers. Conventional Commits.

## Format: `[ID] [P?] [Story] Description — file: path/to/file`

- **[P]**: Can run in parallel (no shared state)
- Include exact file paths in every task
- Test tasks PRECEDE implementation tasks (TDD)
- Run verify-tasks check after marking tasks complete

---

## Phase 1: Setup

**Purpose**: Create package/module skeleton. No logic yet.
**Checkpoint**: `[PROJECT.build_command]` succeeds with zero errors.

- [ ] T001 Create `[package/module]` with copyright header and package declaration — file: `[path]`
- [ ] T002 [P] Add type/interface definitions — file: `[path]`

---

## Phase 2: Tests First (TDD — write before implementation)

**Purpose**: Red phase. Tests fail with "not implemented" or compile errors.
**Checkpoint**: Tests compile but fail.

- [ ] T003 Write unit test for [function]: [what it tests] — file: `[test file path]`
- [ ] T004 [P] Write unit test for [edge case] — file: `[test file path]`

---

## Phase 3: Implementation

**Purpose**: Green phase. Implement to make tests pass.
**Checkpoint**: `[PROJECT.test_command]` passes with zero failures.

- [ ] T005 Implement [function]: [what it does] — file: `[path]`
- [ ] T006 [P] Implement [function]: [what it does] — file: `[path]`

---

## Phase 4: Integration

**Purpose**: Wire into the main system. Validate end-to-end.
**Checkpoint**: Full test suite passes. Acceptance scenarios from spec validated.

- [ ] T007 Register [component] in system setup — file: `[setup file path]`
- [ ] T008 Write integration test — file: `[integration test path]`

---

## Phase 5: Journey Validation

**Purpose**: Confirm this feature advances the journeys listed in the spec header.
**Checkpoint**: Relevant journey steps in `docs/aide/definition-of-done.md` produce documented output.

- [ ] T009 Run relevant journey steps and capture output
- [ ] T010 Verify all [X] tasks have real implementation — zero phantom completions
- [ ] T011 All acceptance criteria from spec.md pass
- [ ] T012 Journey output added to PR body as evidence
