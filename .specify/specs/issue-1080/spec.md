# Spec: Monoculture break — adversary check script

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: Monoculture break: adversarial agent role for architecture reviews (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **O1 — Script exists and is executable**: `scripts/adversary-check.sh` exists, is `chmod +x`, and
   runs without error when given a valid issue number.
   *Violation*: script missing, not executable, or exits non-zero on valid input.

2. **O2 — Adversary output is structured**: the script emits a structured block starting with
   `[🔴 ADVERSARY]` containing at minimum one `WHAT WOULD BREAK THIS:` line and a `VERDICT:` line
   (`PROCEED` or `CHALLENGE`).
   *Violation*: output is unstructured free-form text.

3. **O3 — Adversary asks three concrete failure-mode questions**: the script evaluates each proposed
   queue item against exactly three adversarial lenses:
   (a) "What is the exact mechanism?" — forces naming the API, CRD field, or function
   (b) "What is the blast radius?" — forces enumerating what breaks if this is wrong
   (c) "What competing approach exists?" — forces comparison against an external reference
   *Violation*: any of the three questions is absent from the output.

4. **O4 — Script is idempotent**: running the script twice on the same issue produces the same
   verdict structure (PROCEED/CHALLENGE). Non-deterministic output is a violation.
   *Violation*: repeated runs change VERDICT without any input change.

5. **O5 — Script fails safe**: when the script cannot retrieve issue details (network error, bad
   issue number, missing `gh`), it exits 0 and emits `[ADVERSARY SKIPPED — <reason>]`. It never
   blocks queue generation due to its own failure.
   *Violation*: script exits non-zero or hangs on missing issue input.

6. **O6 — COORDINATOR hook documented**: `otherness-config.yaml` gains an `adversary` section
   with `enabled: true/false` and `script:` path. COORDINATOR reads this to decide whether to
   invoke the adversary check.
   *Violation*: no adversary section in config; COORDINATOR has no way to discover the script.

---

## Zone 2 — Implementer's judgment

- How verbose the adversary output is (brief bullets vs long paragraphs)
- Whether VERDICT=CHALLENGE causes the COORDINATOR to skip the item or just log a warning
- Whether the script reads the full issue body or just the title
- The internal logic for determining PROCEED vs CHALLENGE (heuristics are fine)

---

## Zone 3 — Scoped out

- Actual model invocation: the adversary check runs as a deterministic script, not an LLM call
- Persistent adversary history across sessions
- Integration with GitHub PR review process
- Blocking merges on CHALLENGE verdict (the adversary only challenges at queue-gen time)
