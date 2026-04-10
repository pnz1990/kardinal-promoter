# Queue 007 — Stage 7 (Health Adapters) + Stage 8 CLI Part 2 + Stage 10 (PR Evidence)

> **Generated**: 2026-04-10
> **Stage**: Stage 7 (Health), Stage 8 full CLI, Stage 10 (PR Evidence)
> **Roadmap ref**: docs/aide/roadmap.md Stages 7, 8, 10
> **Batch size**: 3 items (014+015+016 can run in parallel — all depend only on 013 which is done)
> **Status**: active

---

## Purpose

With Stage 5+6 complete, the full promotion loop runs end-to-end but with a stub health
check. This queue delivers the three most impactful features to make J1 (Quickstart)
pass:

1. **014** — Real health verification (replaces stub) → J1 can pass
2. **015** — Full CLI commands (create, rollback, policy simulate) → J5 passes
3. **016** — Full PR evidence body and GitHub labels → J1 PR quality

Dependencies satisfied:
- 014 depends on 013 (done ✅)
- 015 depends on 013 (done ✅)
- 016 depends on 013 (done ✅)

All three can be assigned immediately and worked in parallel.

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 014 | 014-health-adapters | Health Adapters — Deployment, Argo CD, Flux | 013 (merged) | immediately |
| 015 | 015-cli-full | Full CLI — create bundle, policy, rollback, pause/resume | 013 (merged) | immediately |
| 016 | 016-pr-evidence | PR Evidence, Labels, and Webhook Reliability | 013 (merged) | immediately |

---

## Assignment

All 3 items assigned to STANDALONE-ENG sequentially (standalone mode, max 1 concurrent).

Order: 014 → 015 → 016 (health is most critical for J1)

---

## Acceptance Gate

Queue closes when:
- All 3 items state=done in state.json
- `go build ./...` passes on main
- `go test ./... -race` passes on main
- J1 journey health check step passes with a real Deployment
