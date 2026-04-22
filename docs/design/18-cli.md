# 18: CLI — `kardinal` command

> Status: Complete | Created: 2026-04-22
> See also: `cmd/kardinal/`, `docs/cli-reference.md`

---

## What this does

Provides the `kardinal` CLI for developers to interact with the promotion controller: create bundles, inspect pipeline state, simulate policy gates, trigger rollbacks.

---

## Present (✅)

- ✅ **`kardinal create bundle`**: creates a Bundle CRD from `--image`, `--pipeline`, `--author`, `--commit-sha`, `--ci-run-url` flags.
- ✅ **`kardinal get pipelines`**: lists all Pipelines with their Bundle phase per environment (table format).
- ✅ **`kardinal explain <pipeline> --env <env>`**: shows gate details — expression, current evaluation result, last-evaluated timestamp.
- ✅ **`kardinal policy simulate --pipeline <p> --env <e> --time "<day> <time>"`**: evaluates PolicyGate CEL expressions at a given simulated time. Returns `RESULT: BLOCKED` or `RESULT: ALLOWED`.
- ✅ **`kardinal rollback <pipeline> --env <env>`**: opens a rollback PR targeting the previous Bundle version.
- ✅ **`kardinal pause <pipeline>` / `kardinal resume <pipeline>`**: sets `Bundle.spec.paused=true/false`.
- ✅ **`kardinal version`**: prints version string.
- ✅ **Output formats**: `--output json` flag for machine-readable output.

---

## Future (🔲)

- 🔲 **`kardinal init`** — scaffold a `pipeline.yaml` and `policy-gates.yaml` from a wizard. Stage 11 work item.
- 🔲 **`kardinal status`** — alias for `get pipelines` with richer status output.

---

## Zone 1 — Obligations

**O1** — All commands match the output format described in `docs/cli-reference.md`.
**O2** — `kardinal policy simulate` returns the exact strings `RESULT: BLOCKED` or `RESULT: ALLOWED`.
**O3** — `--output json` produces valid JSON parseable by `jq`.
