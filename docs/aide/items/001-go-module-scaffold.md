# Item 001: Go Module, Directory Layout, and Makefile

> **Queue**: queue-001 (Stage 0)
> **Branch**: `001-go-module-scaffold`
> **Depends on**: nothing
> **Assignable**: immediately
> **Contributes to**: All journeys (foundation)

---

## Goal

Create the project skeleton that every subsequent stage builds on. After this item,
`go build ./...` works with the canonical directory layout and Makefile targets exist.

No controller logic. No CRDs. Scaffold only.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 0

---

## Deliverables

1. `go.mod` with module `github.com/kardinal-promoter/kardinal-promoter`, `go 1.23`
2. `go.sum` (empty or with initial deps)
3. Canonical directory layout (empty Go files with correct package declarations):
   ```
   cmd/kardinal-controller/main.go   (package main, stub main())
   cmd/kardinal/main.go              (package main, stub main())
   internal/api/                     (empty — types come in Stage 1)
   internal/controller/              (empty)
   internal/cel/                     (empty)
   internal/git/                     (empty)
   internal/health/                  (empty)
   internal/steps/                   (empty)
   internal/graph/                   (empty)
   config/crd/bases/                 (empty)
   config/rbac/                      (empty)
   config/manager/                   (empty)
   examples/quickstart/              (empty placeholder)
   examples/multi-cluster-fleet/     (empty placeholder)
   ```
4. `Makefile` with targets:
   - `generate`: runs `controller-gen` (stub that echoes "not yet" until stage 1)
   - `manifests`: runs `controller-gen` CRD generation (stub)
   - `build`: `go build -o bin/kardinal-controller ./cmd/kardinal-controller && go build -o bin/kardinal ./cmd/kardinal`
   - `test`: `go test ./... -race -count=1 -timeout 120s`
   - `lint`: `go vet ./...`
   - `docker-build`: stubs to `docker build .`
   - `install`: stubs to `kubectl apply -f config/crd/bases/`
   - `.PHONY` declarations for all targets
5. Initial Go dependencies in `go.mod` for downstream stages:
   - `sigs.k8s.io/controller-runtime` (latest stable)
   - `k8s.io/client-go`
   - `k8s.io/api`
   - `k8s.io/apimachinery`
   - `github.com/rs/zerolog`
   - `github.com/spf13/cobra`
6. Apache 2.0 copyright header on every `.go` file created
7. `.gitignore` covering: `bin/`, `vendor/`, `*.test`, `*.out`

---

## Acceptance Criteria

- [ ] `go build ./...` succeeds with zero errors on `linux/amd64`
- [ ] `go test ./... -race` succeeds (no tests yet = zero failures)
- [ ] `go vet ./...` reports zero warnings
- [ ] `make build` produces `bin/kardinal-controller` and `bin/kardinal`
- [ ] `make test` exits 0
- [ ] All `.go` files have Apache 2.0 copyright header
- [ ] Directory layout matches the canonical layout above
- [ ] No banned filenames (`util.go`, `helpers.go`, `common.go`)

---

## Self-Validation Commands

```bash
make build
make test
make lint
go mod tidy && git diff go.sum  # must be empty after tidy
```

---

## Journey Contribution

This item is a prerequisite for all journeys. No journey step passes until this item
produces a compilable module.
