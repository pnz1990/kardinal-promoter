# Queue 006 — Stage 5 + 6 (Git Operations, Steps Engine, PromotionStep Reconciler)

> **Generated**: 2026-04-10
> **Stage**: Stage 5 — Git Operations + GitHub PR Flow; Stage 6 — PromotionStep Reconciler
> **Roadmap ref**: docs/aide/roadmap.md Stages 5, 6
> **Batch size**: 2 items (serial — 013 depends on 012)
> **Status**: active

---

## Purpose

Stage 5 (Git operations, SCM provider, steps engine) is the critical path to J1
(Quickstart). Once the steps engine is done, Stage 6 (PromotionStep reconciler)
wires the full promotion loop end-to-end.

Dependencies satisfied:
- 012 depends on 010 (done ✅) + 009 (done ✅)
- 013 depends on 012 (new)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 012 | 012-scm-and-steps-engine | SCM Provider + Steps Engine + Git Built-ins | 010 (merged) | immediately |
| 013 | 013-promotionstep-reconciler | PromotionStep Reconciler — Full Promotion Loop | 012 (merged) | after 012 |

---

## Assignment

- **012-scm-and-steps-engine** → STANDALONE-ENG (Stage 5 core)
- **013-promotionstep-reconciler** → STANDALONE-ENG (after 012 done)

---

## Acceptance Gate

Both items `done` before advancing to Stage 7 queue.

- Steps engine executes all 7 built-in steps
- PromotionStep reconciler drives full state machine
- Bundle reaches Verified in integration test with mock GitHub
- `go test ./... -race` passes
