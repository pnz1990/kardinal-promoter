# Project Constitution

This document governs all autonomous AI agents working on this project.
It is read at the start of every session before any work begins.
It overrides all other instructions.

---

## I. Kubernetes is the Control Plane
*(project-specific — override in AGENTS.md if not applicable)*

Every object in the system is a Kubernetes CRD. etcd is the database. The API
server is the API. There is no external database, no dedicated API server, and no
state outside of etcd. CLI, UI, and webhook endpoints are convenience layers that
create and read CRDs. A user can operate the entire system with kubectl.

## II. Never Mutate Workload Resources
*(project-specific — override in AGENTS.md if not applicable)*

The controller never creates, updates, or deletes workload resources (Deployments,
Services, HTTPRoutes) directly. All cluster state changes flow through Git.

## III. Pluggable by Default
*(project-specific — override in AGENTS.md if not applicable)*

Every integration point is a Go interface. Phase 1 ships one implementation per
interface. Adding a provider means implementing the interface and registering it.
No refactoring of core logic.

## IV. Single Source of Truth per Layer

The project's source of truth is layered:
1. `docs/aide/vision.md` — product intent (human-owned)
2. `docs/aide/roadmap.md` — delivery plan (human-owned)
3. `docs/aide/definition-of-done.md` — acceptance criteria (journeys)
4. `docs/aide/team.yml` — team process and roles
5. `.specify/memory/constitution.md` — this file
6. `.specify/memory/sdlc.md` — the reusable SDLC process
7. `AGENTS.md` — project-specific agent context

Higher layers override lower layers. The vision overrides the roadmap. The constitution overrides team.yml.

## V. The PR is the Approval Surface

Every change that requires human approval produces a Git pull request. The PR body
contains evidence of what was done and why it is safe to merge. Human approval
happens by merging the PR. No other approval surface is required.

## VI. Artifacts Over Diffs

Promotions, deployments, and releases move versioned, immutable artifacts with
provenance — not opaque Git diffs. Every artifact carries: what it is, who built
it, when, from what source.

## VII. Rollback is a Forward Operation

Rolling back is promoting a previous artifact through the same pipeline, same
gates, same audit trail. There is no separate rollback mechanism.

## VIII. Code Standards
*(project-specific — override in AGENTS.md for non-Go projects)*

- Copyright header on every source file
- Structured error handling with context wrapping
- Structured logging with contextual fields (no fmt.Println)
- Table-driven tests with assertions, race detection enabled
- Conventional Commits: `type(scope): description`
- No utility files (`util.go`, `helpers.go`, `common.go`)
- Every reconciler / handler is idempotent

## IX. Autonomous Development is Non-Negotiable

This project is built 100% by autonomous AI agents. No human writes code, specs,
or tests. Agents work in parallel in isolated git worktrees, validate each other's
work via PR review, and work backwards from user documentation and examples.

If a feature is not described in `docs/`, `examples/`, or `.specify/specs/`, it
does not exist. The implementation serves the documentation, not the other way
around.

Any agent that skips tests, marks tasks complete without implementation, or
deviates from the spec without escalating is in violation of this principle.

## X. The Definition of Done is the North Star

The project is complete when all journeys in `docs/aide/definition-of-done.md`
pass end-to-end. Unit tests are necessary but not sufficient.

A feature is done when its journey test passes — not when its spec is satisfied
in isolation. Every implementation decision is made in service of making a journey
pass.

## XI. The SDLC Process is Reusable

The SDLC process defined in `.specify/memory/sdlc.md` and `docs/aide/team.yml`
is project-agnostic. The process applies to any project. The project-specific
context (tech stack, commands, package layout, journey steps) lives in `AGENTS.md`
and `docs/aide/definition-of-done.md`.

When starting a new project, copy the SDLC files (listed in sdlc.md) and replace
only the project-specific context.

---

## Architecture Reference

See `AGENTS.md` for:
- Project-specific tech stack and architecture
- Package layout
- Language-specific code standards
- Journey validation commands
- Anti-patterns specific to this project

## Spec Inventory

Maintained in `docs/aide/progress.md`.
