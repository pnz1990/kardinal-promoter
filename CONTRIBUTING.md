# Contributing to kardinal-promoter

Thank you for your interest in contributing to kardinal-promoter!

## Getting Started

### Prerequisites

- Go 1.23+
- `kubectl` and a Kubernetes cluster (kind recommended for local development)
- `helm` for chart-related changes

### Build and Test

```bash
# Build all binaries
go build ./...

# Run all tests (with race detector)
go test ./... -race -count=1 -timeout 120s

# Lint
go vet ./...
```

### Run Locally

```bash
# Start a local kind cluster with all dependencies
make setup-e2e-env

# Build and run the controller
go run ./cmd/kardinal-controller/...
```

## How to Contribute

### Bug Reports

Open a [GitHub Issue](https://github.com/pnz1990/kardinal-promoter/issues/new/choose)
using the **Bug Report** template. Include:
- The exact commands you ran
- The expected vs. actual behavior
- Your Kubernetes version and how you installed kardinal-promoter

### Feature Requests

Open a [GitHub Issue](https://github.com/pnz1990/kardinal-promoter/issues/new/choose)
using the **Feature Request** template. Describe the problem you are solving, not just
the solution you have in mind.

### Pull Requests

1. Fork the repository and create a branch from `main`.
2. Make your changes. Every PR must include:
   - Tests for new behavior
   - Updated documentation in `docs/`
   - Apache 2.0 license header in new Go files
3. Ensure `go build ./...`, `go test ./... -race`, and `go vet ./...` all pass.
4. Open a pull request against `main`.

PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/):
`feat(scope): description`, `fix(scope): description`, `chore(scope): description`.

## Code Standards

- No bare `errors.New` — use `fmt.Errorf("context: %w", err)`
- Use `zerolog.Ctx(ctx)` for logging — no `fmt.Println`
- Table-driven tests with `testify/assert` and `require`
- No `util.go`, `helpers.go`, or `common.go` filenames
- Apache 2.0 copyright header in every new `.go` file:

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```

## QA Process

### QA Docs Gate

`scripts/qa-docs-gate.sh` is run during QA review (Phase 3, §3b) to verify that
user-visible features include updated documentation:

```bash
# Run against a PR number
PR_NUM=1234 ./scripts/qa-docs-gate.sh

# Or pass as an argument
./scripts/qa-docs-gate.sh 1234 pnz1990/kardinal-promoter
```

**Exit codes:**
- `0` — all checks pass, or the script was skipped (missing PR number, no `gh` CLI)
- `1` — **WRONG finding**: a user-visible feature (CLI command, CRD field, UI page) was
  promoted from `🔲 Future` to `✅ Present` in a design doc, but no corresponding
  `docs/` file was added or updated

**When is it skipped (Layer 1 auto-documented exemption)?**

The script does not block when the changed feature is already documented by auto-generated
files (`docs/cli-reference.md` updated by CI, API docs from code comments, etc.).
See `scripts/qa-docs-gate.sh` header comments for the full Layer 1 exemption list.

**When to invoke it:**

The QA reviewer runs this script after the spec conformance check (§3b) on any PR that:
- Adds a new CLI subcommand or flag
- Adds or changes a CRD spec field visible to users
- Adds a new UI page or changes an existing UI workflow
- Moves a `🔲 Future` item to `✅ Present` in any `docs/design/` file

## Community

Questions and discussions:

- **GitHub Issues**: bug reports and feature requests
- **GitHub Discussions**: getting started help, show & tell, Q&A
  (coming soon — see [issue #979](https://github.com/pnz1990/kardinal-promoter/issues/979))

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE).
