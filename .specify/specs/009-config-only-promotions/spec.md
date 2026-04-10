# Feature Specification: Config-Only Promotions

**Feature Branch**: `009-config-only-promotions`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 008-promotion-steps-engine, 002-pipeline-translator
**Design doc**: `docs/design/09-config-only-promotions.md`
**Contributes to journey(s)**: J2 (config-only promotions used in fleet scenarios)
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

Config-only promotions allow teams to promote configuration changes (resource limits, env vars, feature flags) through the same Pipeline and PolicyGate flow as image promotions. Bundle `type: config` references a Git commit SHA. The `config-merge` step applies changes via cherry-pick or overlay.

---

## User Scenarios & Testing

### User Story 1 — Config Bundle promotes a Git commit (Priority: P1)

A Bundle with `type: config` and `artifacts.gitCommit.sha` runs through the same Pipeline as an image Bundle, using the `config-merge` step instead of `kustomize-set-image`.

**Independent Test**: `go test ./pkg/steps/steps/... -run TestConfigMerge`

**Acceptance Scenarios**:

1. **Given** a config Bundle, **When** `InferDefaultSteps()` is called, **Then** the step sequence includes `config-merge` instead of `kustomize-set-image`
2. **Given** `config-merge` with `strategy: overlay`, **When** a source commit modifies `configs/my-app/deployment.yaml`, **Then** that file is copied into the environment directory
3. **Given** `config-merge` with `strategy: cherry-pick` on a conflicted file, **When** run, **Then** returns StepFailed with conflict details

---

### User Story 2 — Config Bundle does not supersede image Bundle (Priority: P1)

A new config Bundle arriving while an image Bundle is promoting does not interrupt the image promotion.

**Independent Test**: `go test ./pkg/reconciler/bundle/... -run TestConfigDoesNotSupersedeImage`

**Acceptance Scenarios**:

1. **Given** an image Bundle in Promoting state, **When** a new config Bundle is created, **Then** the image Bundle continues uninterrupted
2. **Given** a config Bundle in Promoting state, **When** a newer config Bundle is created, **Then** the older config Bundle is superseded (same type)

---

### User Story 3 — PR body shows config evidence (Priority: P2)

PRs for config promotions show the config commit message and changed files, not image digest.

**Acceptance Scenarios**:

1. **Given** a config Bundle with `artifacts.gitCommit.message: "Update resource limits"`, **When** the PR is opened, **Then** the PR body shows the commit message and list of changed files

---

### Edge Cases

- Cherry-pick conflict: step fails with details, operator resolves manually
- Empty diff (no files changed in environment path): step succeeds as no-op
- Config repo needs different credentials: use `artifacts.gitCommit.secretRef`

---

## Requirements

- **FR-001**: Bundle `spec.type` field: "image" (default) or "config"
- **FR-002**: `config-merge` step: cherry-pick (default) or overlay strategy
- **FR-003**: `git-clone` step: for config Bundles, also clones the config source repo
- **FR-004**: Superseding: config and image Bundles do not supersede each other
- **FR-005**: PR body for config: commit message + changed files (not image info)

---

## Success Criteria

- **SC-001**: `go test ./pkg/steps/... ./pkg/reconciler/... -race` passes
- **SC-002**: All 12 unit test cases from design doc pass
- **SC-003**: Apache 2.0 header on every .go file
