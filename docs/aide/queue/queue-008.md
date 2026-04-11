# Queue 008 — Stage 11 part 1 + Stage 10 completion

> **Generated**: 2026-04-11
> **Stage**: Stage 11 (kardinal init), Stage 10 completion (startup reconciliation)
> **Roadmap ref**: docs/aide/roadmap.md Stages 10, 11
> **Batch size**: 2 items (017 → 018 sequential)
> **Status**: active

---

## Purpose

With Stages 0-8 and 10 complete, the v0.2.0 milestone has one remaining epic:
Epic #42 (Quickstart working end-to-end). This queue delivers:

1. **017** — `kardinal init` wizard + quickstart example/docs updated → closes Epic #42, enables J1
2. **018** — Startup reconciliation + webhook health endpoint → completes Stage 10, improves J1 reliability

Dependencies satisfied:
- 017 depends on 015 (done ✅) and 016 (done ✅)
- 018 depends on 016 (done ✅)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 017 | 017-kardinal-init | `kardinal init` + Quickstart docs | 015, 016 (merged) | immediately |
| 018 | 018-startup-reconciliation | Startup reconciliation + webhook health | 016 (merged) | after 017 |

---

## Assignment

017 → 018 sequential (standalone mode).

---

## Acceptance Gate

Queue closes when:
- Both items state=done in state.json
- `go build ./...` passes on main
- `go test ./... -race` passes on main
- J1 journey steps 1-8 from definition-of-done.md are documented with working commands
