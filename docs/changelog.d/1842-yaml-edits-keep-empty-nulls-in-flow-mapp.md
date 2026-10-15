---
section: Fixed
---

- **YAML edits keep empty nulls in flow mappings** (#1842) — the yaml, helm and kustomize update strategies wrote a null with no text in a flow mapping (`{a: }`, `{a}`) back as an empty string (`{a: ''}`); it is now written as `null`.
