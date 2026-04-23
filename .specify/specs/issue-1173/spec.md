# Spec: issue-1173

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: COORDINATOR alphabetic doc ordering starves doc 15 (production-readiness) items permanently (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `coord.md §1c` `ISSUE_GEN` block uses round-robin source selection: items from
different design docs are interleaved such that no single doc exhausts its items before
others get representation. Specifically: `sorted(os.listdir(design_dir))` is replaced
with a priority-weighted interleaver that produces at least one doc-15 item per queue
generation when doc-15 has unclaimed items.

Falsified by: running the queue-gen with only doc-12 (40 items) and doc-15 (5 items)
populated — if 0 doc-15 items appear in the first 10 created issues, O1 fails.

**O2**: `coord.md §1c` display block (the first `PYEOF` block) uses the same round-robin
ordering so diagnostic output reflects the actual selection order.

Falsified by: the diagnostic `ITEM: ...` lines showing only doc-12/13 items when doc-15
has unclaimed items.

**O3**: Design doc `docs/design/12-autonomous-loop-discipline.md` is updated: the
`COORDINATOR alphabetic doc ordering starves doc 15` item is flipped from `🔲` to `✅`
with a PR reference.

Falsified by: `grep -q '✅.*COORDINATOR alphabetic doc ordering' docs/design/12-autonomous-loop-discipline.md` returning non-zero.

**O4**: The change is in `coord.md` which lives at `~/.otherness/agents/phases/coord.md`.
The PR title begins with `feat(coord):`. The PR description includes this spec file's
verification notes.

Falsified by: PR not found with `feat(coord):` prefix.

---

## Zone 2 — Implementer's judgment

- Priority weight for `15-production-readiness.md`: 2× (minimum one item per queue of N≥3).
  Other priority docs (if identified): can be added similarly.
- Round-robin interleaving strategy: collect one item from each doc in doc-number order,
  then repeat. This ensures spatial diversity across all docs.
- The "spatial diversity" block already in ISSUE_GEN (`seen_sources` tracking) accomplishes
  the first-pass round-robin but then falls back to ordered exhaustion. We can remove the
  two-phase approach and replace with a single priority-weighted round-robin.

---

## Zone 3 — Scoped out

- Runtime claim (§1e) ordering is not changed — claim order is already priority-weighted.
- `§1b-vision` VISION_PRESSURE_SET computation is not changed.
- `§1c-guard` chore-only queue guard is not changed.
- This spec does not change how items are claimed, only how they are generated.

---

## Verification note

Verified by: `grep -q 'doc_priority' ~/.otherness/agents/phases/coord.md` — confirms the
priority weighting variable is present in the modified coord.md. This is a pure process
change to the agent loop; no Go/UI code changes.
