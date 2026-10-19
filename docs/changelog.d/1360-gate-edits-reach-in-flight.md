---
section: Changed
---

- **Creating, editing or deleting a PolicyGate reaches the versions already promoting** (#1360) — kardinal copies the gates again into every promoting version whose gates changed, in place, so a fixed or tightened gate applies at once to the environments that have not started. Verified, superseded, rejected and failed versions keep the gates they had, and an override on a version's gate stays. A gate change that a version cannot be built with does not fail it: it keeps its gates, and `GraphSynced` says why. Each copy of a gate now carries the annotations `kardinal.io/gate-template-generation` and `kardinal.io/gate-template-digest`, which name the revision of the gate it was copied from; an edit reverted before a version saw it still moves the copies to the gate's current generation (#2145) ([When a new or edited gate takes effect](policy-gates.md#when-a-new-or-edited-gate-takes-effect)).
