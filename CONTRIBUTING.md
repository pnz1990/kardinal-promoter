# Contributing to kardinal-promoter

Thank you for your interest in contributing to kardinal-promoter!

## Getting Started

### Prerequisites

- Go 1.26+ (the exact toolchain is pinned in `go.mod`)
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

A PR from anyone other than the maintainer that changes `AGENTS.md`, `CLAUDE.md`, `.claude/`,
`.github/workflows/` or `.github/actions/` fails the **agent instructions guard** check. Those
files are instructions for coding agents or run with repository secrets, so the maintainer makes
those changes; describe what you need in an issue or in the PR.

PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/):
`feat(scope): description`, `fix(scope): description`, `chore(scope): description`.

## Code Standards

- Wrap errors with context: `fmt.Errorf("context: %w", err)`. Use `errors.New` only for package-level sentinel errors (`var ErrX = errors.New(...)`) and permanent step failures
- Use `zerolog.Ctx(ctx)` for logging — no `fmt.Println`
- Table-driven tests with `testify/assert` and `require`
- No `util.go`, `helpers.go`, or `common.go` filenames
- Apache 2.0 copyright header in every new `.go` file:

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```

## Community

Questions, bugs and ideas: open a [GitHub Issue](https://github.com/pnz1990/kardinal-promoter/issues)
(label `question` for questions).

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE).
