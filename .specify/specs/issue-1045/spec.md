# Spec: issue-1045 — SM PDCA workflow result check

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: PDCA workflow result is not read by the SM (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — The SM §4f health computation block MUST query `gh run list --workflow=pdca.yml`
for the latest PDCA run conclusion before finalising the health signal.

**O2** — If the latest PDCA run conclusion is `"failure"`, the SM MUST set `HEALTH="RED"`.
It is not sufficient to set AMBER; a failing PDCA means end-to-end journeys are broken.

**O3** — The PDCA check MUST be fail-open: if the `gh run list` command fails (API error,
no runs found, workflow absent), health is left unchanged and a non-fatal log line is emitted.
The loop must not stall because pdca.yml has never run.

**O4** — When PDCA_STATUS is failure and HEALTH is RED, the SM MUST open a GitHub issue
with title `[PDCA FAILING] Loop shipped PRs that broke end-to-end journeys` (deduplicated:
check for existing open issue with this title fragment before creating).

**O5** — The PDCA check value (`pdca=PASS|FAIL|unknown`) MUST appear in the condensed
batch report line so humans can see it at a glance.

---

## Zone 2 — Implementer's judgment

- Where to insert: immediately after `[ "$CI_STATUS" = "failure" ] && HEALTH="AMBER"` and
  before the NEEDS_HUMAN_COUNT check in §4f. PDCA failure is RED (not AMBER) because CI
  unit tests passing + PDCA failing means product is broken.
- The `pdca=RED` signal in the COORD check: COORD reads the HEALTH from state. If `HEALTH=RED`
  and the cause is PDCA, COORD should skip new queue generation. This is implemented by
  existing COORD behaviour (CI red → do not generate new items) — no COORD change is needed
  as long as the SM writes `pdca_status` to state.json for COORD to read.
- Whether to check the last N runs or only the last 1: last 1 is sufficient. The workflow
  runs on schedule; if the last run failed, the product is broken now.

---

## Zone 3 — Scoped out

- Triggering a PDCA re-run automatically (requires workflow dispatch, out of scope here)
- Parsing the PDCA log to identify which journey failed (future enhancement)
- Changes to pdca.yml itself
