# Queue 016 — Workshop 2 Scope: Promote Command + ArgoRollouts + Critical Fixes

> Created: 2026-04-11
> Status: Active
> Purpose: Enable Workshop 2 (multi-cluster fleet with Argo Rollouts) and fix critical arch debt

## Items

| Item | Title | Priority | Size | Depends on |
|---|---|---|---|---|
| 033-promote-command | Add kardinal promote command (#119) | high | s | 011, 015 |
| 034-fix-cel-import-ban | Fix policy simulate CEL import ban (#137) | critical | m | 010 |
| 035-argo-rollouts-adapter | Implement argoRollouts health adapter (#118) | critical | l | 014 |
| 036-prstatuscrd | Introduce PRStatus CRD for Graph-observable merge signal (#133) | critical | l | 012, 013 |

## Notes

Workshop 2 requires:
- `kardinal promote` command for manually triggering promotions
- argoRollouts health adapter for Argo Rollouts canary health verification
- PRStatus CRD to make PR merge observable by the krocodile Graph
- CEL import ban fix (affects code quality and future safety)

All four items are independent and can be worked in parallel.

After this queue, the coordinator should assign Workshop 2 execution as an item.
