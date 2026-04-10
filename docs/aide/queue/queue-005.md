# Queue 005 — Stage 4 (PolicyGate CEL) + Stage 8 partial (CLI)

> **Generated**: 2026-04-10
> **Stage**: Stage 4 — PolicyGate CEL Evaluator + Stage 8 partial — CLI Foundation
> **Roadmap ref**: docs/aide/roadmap.md Stages 4, 8
> **Batch size**: 2 items (parallel)
> **Status**: active

---

## Purpose

Stage 4 (PolicyGate CEL) and Stage 8 partial (CLI foundation) are independent and
can be implemented in parallel. Both Stage 4 and Stage 8 dependencies are satisfied:
- Stage 4 depends on Stage 3 (done ✅)
- Stage 8 depends on Stage 2 (done ✅)

Stage 5 (Git operations) also depends on Stage 3 but is larger — it will be
queue-006 after these items are done.

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 010 | 010-policygate-cel | PolicyGate CEL Evaluator | 009 (merged) | immediately |
| 011 | 011-cli-foundation | CLI Foundation — version, get pipelines/bundles/steps | 005 (merged) | immediately |

---

## Assignment Wave 1 (both items assignable immediately, parallel)

- **010-policygate-cel** → STANDALONE-ENG (Stage 4 core)
- **011-cli-foundation** → STANDALONE-ENG (after 010 done, serial in standalone mode)

---

## Acceptance Gate

Both items `done` before advancing to Stage 5 queue.

- PolicyGate reconciler evaluates CEL, patches status, requeues
- Weekend gate and author gate tests pass
- CLI binary builds and `kardinal version` / `get pipelines` work
- `go test ./... -race` passes
