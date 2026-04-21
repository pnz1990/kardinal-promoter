# Spec: issue-973 — kardinal status <pipeline> shows in-flight promotion details

## Design reference

- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 3: Observability`
- **Design item**: No `kardinal status` command for in-flight promotion details (🔲 → ✅)

## Summary

Extend `kardinal status` to accept an optional pipeline name argument. When provided,
show in-flight promotion details: active bundle, PromotionStep states with active step
highlighted, blocking PolicyGates with CEL expression and reason.

---

## Zone 1 — Obligations

### O1: `kardinal status [pipeline]` command updated
- `Use: "status [pipeline]"` — MaximumNArgs(1)
- No arg → existing cluster summary (unchanged)
- With arg → per-pipeline in-flight view

### O2: Per-pipeline output shows:
- `Pipeline: <name>  Namespace: <ns>`
- `Active bundle(s): <names>` (comma-separated)
- "Promotion Steps" table: ENVIRONMENT | STATE | ACTIVE STEP | PR | AGE
- In-progress states (Promoting, WaitingForMerge, HealthChecking) marked with `▶`
- ACTIVE STEP = first non-terminal step name from `status.steps[]`
- "No active promotions." when no PromotionSteps exist

### O3: Blocking PolicyGates shown when `status.ready == false`
- "Blocking Policy Gates" section with GATE | ENV | EXPRESSION | REASON | LAST CHECKED
- ENV from `kardinal.io/environment` label
- Expressions truncated at 40 chars

### O4: Error for missing pipeline
- Returns error with "not found" message when pipeline doesn't exist in namespace

### O5: Tests (5 passing)
- NoSteps → "No active promotions."
- NotFound → error with "not found"
- ActiveStep → ▶ marker, active step name, PR URL visible
- BlockingGate → gate table shown
- TerminalSteps → "terminal state" hint

### O6: Design doc updated (🔲 → ✅)

---

## Zone 2 — Constraints

- `kardinal status` (no args) output unchanged — backwards compatible
- `statusPipelineWriter` is a standalone function accepting `(io.Writer, sigs_client.Client, ns, pipeline)` — testable without CLI machinery

---

## Zone 3 — Out of scope

- `--bundle <name>` filter (design doc says "optional" — deferred; all active bundles shown)
- Per-bundle CEL variable values in gate output (design doc §Lens 8 — deferred to logs command)
- JSON/YAML output flag (follow-up)
