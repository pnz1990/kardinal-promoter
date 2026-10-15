---
section: Added
---

- **Fuzz and property tests** (#1837) — the Graph builder (every Graph it builds passes kro v0.10.0-rc.0's compiler rules: node IDs, node shapes, forEach, references, an acyclic dependency graph; and a Pipeline the CRD accepts builds when its environments form no cycle and no fleet target takes another environment's name), the PolicyGate CEL context (every documented variable with its documented type) and the YAML editors (an edit changes only its target, a refused one leaves the file as it was, a repeated one writes the same bytes) are fuzzed in CI.
