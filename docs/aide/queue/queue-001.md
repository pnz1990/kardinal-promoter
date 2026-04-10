# Queue 001 — Stage 0: Project Skeleton

> **Generated**: 2026-04-09
> **Stage**: 0 — Project Skeleton
> **Roadmap ref**: docs/aide/roadmap.md Stage 0
> **Batch size**: 4 items
> **Status**: active

---

## Purpose

Establish the Go module, directory layout, build tooling, kubebuilder scaffolding,
CI workflows, and Docker image pipeline so that every subsequent stage has a
working build baseline with nowhere to step on each other.

No controller logic is implemented in this queue. Only the structural scaffolding
that lets later queues compile and test cleanly.

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 001 | 001-go-module-scaffold | Go module, directory layout, and Makefile | — | yes |
| 002 | 002-kubebuilder-scaffold | kubebuilder CRD scaffold + controller-gen | 001 | after 001 done |
| 003 | 003-dockerfile-and-helm | Dockerfile (multi-stage) + Helm chart skeleton | 001 | after 001 done |
| 004 | 004-ci-and-lint | GitHub Actions CI + golangci-lint config | 001 | after 001 done |

---

## Assignment Wave 1

- **001-go-module-scaffold** → ENGINEER-1 (no dependencies, assign immediately)

## Assignment Wave 2 (after 001 done)

- **002-kubebuilder-scaffold** → ENGINEER-1 (or first free slot)
- **003-dockerfile-and-helm** → ENGINEER-2
- **004-ci-and-lint** → ENGINEER-3

---

## Acceptance Gate

All 4 items `done` in `.maqa/state.json` before advancing to Stage 1 queue.

`make build` must produce `bin/kardinal-controller` and `bin/kardinal`.
`make test` must pass with zero failures.
`make manifests` must be idempotent.
`make docker-build` must succeed.
CI workflow must be green on `main`.
