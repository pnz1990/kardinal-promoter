# 00: Project Skeleton

> Status: Complete | Created: 2026-04-22

---

## What this does

Establishes the Go module, directory layout, build tooling, and CI scaffolding that every subsequent stage builds on.

---

## Present (✅)

- ✅ **Go module `github.com/kardinal-promoter/kardinal-promoter`**: `go.mod` and `go.sum` with Go 1.23+, MIT-compatible dependency tree, `govulncheck`-clean.
- ✅ **Canonical directory layout**: `cmd/kardinal-controller/`, `cmd/kardinal/`, `pkg/`, `web/`, `chart/`, `docs/`, `examples/`, `test/` — matches AGENTS.md Package Layout.
- ✅ **Makefile targets**: `build`, `test`, `lint`, `docker-build`, `helm-package`, `setup-e2e-env`.
- ✅ **CI workflows**: `ci.yml` (build, test, lint, govulncheck, trivy), `docs.yml` (mkdocs-material deploy), `pdca.yml` (E2E journey tests).
- ✅ **Apache 2.0 license headers**: enforced by `preflight` CI step.
- ✅ **Banned filename guard**: `util.go`, `helpers.go`, `common.go` rejected by CI.

---

## Future (🔲)

No known future items for the project skeleton at this time.

---

## Zone 1 — Obligations

**O1** — `go build ./...` succeeds from a clean checkout without external setup.
**O2** — `go test ./... -race` passes with no data races.
**O3** — CI green on main before any new feature branch is opened.
