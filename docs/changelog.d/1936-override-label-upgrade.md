---
section: Upgrade
---

- **A gate override is now written with the label `kardinal.io/override-check: "true"`, and one without it is refused** (#1936) — v0.11 accepted an override added with `kubectl edit` or `kubectl patch` of `spec.overrides`, and from the v0.11 `kardinal override`. From this release the `<release>-override-marker` admission policy refuses any write that adds or changes an override entry unless the gate carries the label, so the `<release>-actor-overrides` policy checks its actor stamp; the refusal names the label and the fix. Upgrade the `kardinal` CLI along with the chart (the v0.12 `kardinal override` and the UI set the label), and make scripts and GitOps tools that write `spec.overrides` set it in the same write ([Overrides](overrides.md))
