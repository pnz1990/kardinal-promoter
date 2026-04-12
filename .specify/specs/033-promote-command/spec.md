# Spec: 033-promote-command

> feature_branch: 033-promote-command
> item: docs/aide/items/033-promote-command.md

## Feature

Add `kardinal promote <pipeline> --env <env>` CLI command.

## User Scenarios

**SC-1 — Given** a pipeline "nginx-demo" with environments [test, uat, prod],
**When** user runs `kardinal promote nginx-demo --env prod`,
**Then** a Bundle is created targeting the prod environment, output shows the bundle name.

**SC-2 — Given** a non-existent pipeline "no-such-pipeline",
**When** user runs `kardinal promote no-such-pipeline --env prod`,
**Then** the command returns exit code 1 with error "pipeline not found: no-such-pipeline".

**SC-3 — Given** a valid pipeline but invalid environment,
**When** user runs `kardinal promote nginx-demo --env nonexistent`,
**Then** the command returns exit code 1 with error "environment not found in pipeline".

## Requirements

- FR-001: `kardinal promote <pipeline> --env <env>` subcommand MUST exist in the cobra CLI
- FR-002: The command MUST create a Bundle CRD with `spec.pipeline = <pipeline>` and the intent targeting `<env>`
- FR-003: On success, the command MUST output: "Promoting <pipeline> to <env>: bundle <name> created"
- FR-004: On pipeline not found, exit code MUST be 1 with descriptive error
- FR-005: On environment not in pipeline, exit code MUST be 1 with descriptive error
- FR-006: `docs/cli-reference.md` MUST document the new command with examples

## Go Package Structure

- `cmd/kardinal/cmd/promote.go` — cobra command definition and handler

## Success Criteria

- SC-1: `kardinal promote nginx-demo --env prod` creates a Bundle and prints confirmation
- SC-2: Exit code 1 on missing pipeline
- SC-3: Exit code 1 on invalid environment
