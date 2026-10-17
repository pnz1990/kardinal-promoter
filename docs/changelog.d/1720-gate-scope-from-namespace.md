---
section: Changed
---

- **A PolicyGate in an org policy namespace is shown as org without the `kardinal.io/scope` label** (#1720) — `kardinal policy list` and the gate's instances now show the scope the gate behaves with: `org` for a gate in one of the controller's `--policy-namespaces` (default `platform-policies`) or labelled `kardinal.io/scope: org`, otherwise its label, `team` by default. Before, an unlabelled gate in `platform-policies` acted as an org gate but was shown as `team`. The label is no longer needed in a policy namespace, and the quickstart's gates drop it. Instances created before the upgrade keep their label until their version is rebuilt ([Org-level gates](policy-gates.md#org-level-gates)).
