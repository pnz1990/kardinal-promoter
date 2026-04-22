# Spec: issue-1079 — SM health state definition: explicit thresholds for GREEN/RED/STALL

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: **SM health state definition: explicit thresholds for GREEN/RED/STALL** (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: A file `docs/aide/health-thresholds.md` exists that defines machine-checkable thresholds
for GREEN, AMBER, RED, and STALL health states. The thresholds are:
- **GREEN**: at least 1 feat/fix/test/docs PR merged in this session AND no CI failure
- **AMBER**: 0 vision PRs this session OR needs-human issues open
- **RED**: workflow CI failed (no agent ran) OR last CI run on main failed
- **STALL**: 3+ consecutive sessions with zero substantive PRs (PRs touching files outside `.otherness/`, `docs/aide/`, `.github/state/`)

**O2**: The `health-thresholds.md` file must be machine-readable: each threshold defined as a
structured entry with fields: `state`, `condition`, `checkable_command` (a shell command that
returns true/false), and `consequence`.

**O3**: The design doc `docs/design/12-autonomous-loop-discipline.md` is updated: the Future item
for "SM health state definition" is moved from 🔲 to ✅, referencing `docs/aide/health-thresholds.md`.

**O4**: The `health-thresholds.md` file is referenced from `docs/aide/team.yml` via a comment
or link. Since `team.yml` is read-only for agents (per AGENTS.md), the reference is added as
a comment in the design doc.

---

## Zone 2 — Implementer's judgment

- `team.yml` is marked as an agent-immutable file in AGENTS.md, so thresholds cannot be added there
- `docs/aide/health-thresholds.md` is the correct alternative location
- The thresholds define the same criteria the SM already computes — this formalizes them
- The STALL threshold requires tracking `consecutive_housekeeping_sessions` in state.json — check if it exists already

---

## Zone 3 — Scoped out

- Modifying `sm.md` to actually enforce the new thresholds (requires push to otherness upstream)
- Retroactively computing STALL status from history
- Adding a `progress_class` field to state.json (tracked separately in the SM)
